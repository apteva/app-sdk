//! Minimal, experimental implementation of the existing Apteva basic app
//! contract. No managed activation, component, grant, or database support yet.
use bytes::Bytes;
use http_body_util::{BodyExt, Full, Limited};
use hyper::{
    body::Incoming, server::conn::http1, service::service_fn, Method, Request, Response, StatusCode,
};
use hyper_util::rt::TokioIo;
use serde_json::{json, Value};
use std::{
    collections::BTreeMap, convert::Infallible, env, error::Error, net::SocketAddr, path::PathBuf,
    sync::Arc, time::Duration,
};
use tokio::{
    net::TcpListener,
    sync::{watch, Semaphore},
    task::JoinSet,
};

pub type AppResult<T> = Result<T, Box<dyn Error + Send + Sync>>;
type Handler = dyn Fn(&Environment, Value) -> Result<Value, String> + Send + Sync;

/// Configuration is immutable and installation-scoped. Never log tokens/config.
/// APTEVA_DATA_DIR belongs to this installation and survives binary replacement.
pub struct Environment {
    pub config: Value,
    pub install_id: String,
    pub project_id: String,
    pub data_dir: PathBuf,
    pub gateway_url: String,
    token: String,
    address: SocketAddr,
}
impl Environment {
    pub fn from_env(manifest: &Value) -> AppResult<Self> {
        // Do not accidentally admit work when Server expects a managed standby.
        for key in [
            "APTEVA_DEPLOYMENT_ID",
            "APTEVA_DEPLOYMENT_GENERATION",
            "APTEVA_RUNTIME_MODE",
            "APTEVA_RESOURCE_POLICY",
        ] {
            if env::var(key).is_ok_and(|v| !v.is_empty()) {
                return Err(format!("this SDK does not implement {key}; use a lifecycle-capable SDK for managed deployments").into());
            }
        }
        let token = env::var("APTEVA_APP_TOKEN").unwrap_or_default();
        if token.is_empty() {
            return Err("APTEVA_APP_TOKEN is required".into());
        }
        let config: Value =
            serde_json::from_str(&env::var("APTEVA_APP_CONFIG").unwrap_or_else(|_| "{}".into()))
                .map_err(|_| "APTEVA_APP_CONFIG must be a JSON object")?;
        if !config.is_object() {
            return Err("APTEVA_APP_CONFIG must be a JSON object".into());
        }
        let host = env::var("APTEVA_BIND_HOST").unwrap_or_else(|_| {
            manifest["runtime"]["bind_host"]
                .as_str()
                .unwrap_or("127.0.0.1")
                .into()
        });
        let port: u16 = env::var("APTEVA_APP_PORT")
            .unwrap_or_else(|_| {
                manifest["runtime"]["port"]
                    .as_u64()
                    .unwrap_or(8080)
                    .to_string()
            })
            .parse()?;
        if port == 0 {
            return Err("APTEVA_APP_PORT must be nonzero".into());
        }
        Ok(Self {
            config,
            token,
            address: SocketAddr::new(host.parse()?, port),
            install_id: env::var("APTEVA_INSTALL_ID").unwrap_or_default(),
            project_id: env::var("APTEVA_PROJECT_ID").unwrap_or_default(),
            gateway_url: env::var("APTEVA_GATEWAY_URL").unwrap_or_default(),
            data_dir: PathBuf::from(env::var("APTEVA_DATA_DIR").unwrap_or_else(|_| ".".into())),
        })
    }
}
struct Tool {
    description: String,
    schema: Value,
    handler: Box<Handler>,
}
pub struct App {
    manifest: Value,
    environment: Environment,
    tools: BTreeMap<String, Tool>,
}
impl App {
    /// Accepts JSON (also valid YAML). Validate the release manifest using the
    /// shared offline `apteva-app-contract validate` command before packaging.
    pub fn from_manifest_json(raw: &str) -> AppResult<Self> {
        let manifest: Value = serde_json::from_str(raw)?;
        if manifest["schema"] != "apteva-app/v1"
            || manifest["name"].as_str().unwrap_or("").is_empty()
            || manifest["version"].as_str().unwrap_or("").is_empty()
        {
            return Err("invalid app manifest identity".into());
        }
        // Refuse unsupported declarations instead of pretending to honor them.
        for key in ["db", "components"] {
            if nonempty(&manifest[key]) {
                return Err(format!("{key} is not supported by the basic Rust SDK").into());
            }
        }
        for key in ["resource_policy", "ingress"] {
            if nonempty(&manifest["runtime"][key]) {
                return Err(format!("runtime.{key} is not supported by the basic Rust SDK").into());
            }
        }
        if nonempty(&manifest["runtime"]["package"]["app_capabilities"]) {
            return Err("managed lifecycle capabilities are not implemented".into());
        }
        if let Some(provides) = manifest["provides"].as_object() {
            if provides
                .iter()
                .any(|(k, v)| k != "mcp_tools" && nonempty(v))
            {
                return Err("this SDK supports only provides.mcp_tools".into());
            }
        }
        if manifest["provides"]["mcp_tools"]
            .as_array()
            .is_some_and(|ts| {
                ts.iter().any(|t| {
                    t.as_object().is_none_or(|fields| {
                        fields.iter().any(|(key, value)| {
                            key != "name" && key != "description" && nonempty(value)
                        })
                    })
                })
            })
        {
            return Err("tool permissions/exposure are not supported".into());
        }
        let environment = Environment::from_env(&manifest)?;
        Ok(Self {
            manifest,
            environment,
            tools: BTreeMap::new(),
        })
    }
    /// Handlers must validate their arguments. Only installation-level tools
    /// without caller-specific grants are supported in this initial SDK.
    pub fn tool(
        mut self,
        name: &str,
        description: &str,
        schema: Value,
        handler: impl Fn(&Environment, Value) -> Result<Value, String> + Send + Sync + 'static,
    ) -> AppResult<Self> {
        if name.is_empty() || self.tools.contains_key(name) || schema["type"] != "object" {
            return Err("invalid or duplicate tool declaration".into());
        }
        self.tools.insert(
            name.into(),
            Tool {
                description: description.into(),
                schema,
                handler: Box::new(handler),
            },
        );
        Ok(self)
    }
    pub async fn run(self) -> AppResult<()> {
        let listener = TcpListener::bind(self.environment.address).await?;
        let app = Arc::new(self);
        let permits = Arc::new(Semaphore::new(64));
        let (stop, stopping) = watch::channel(false);
        let mut connections = JoinSet::new();
        let shutdown = shutdown_signal();
        tokio::pin!(shutdown);
        loop {
            tokio::select! {
                _ = &mut shutdown => break,
                Some(_) = connections.join_next(), if !connections.is_empty() => {},
                accepted = listener.accept() => {
                    let (socket, _) = accepted?;
                    let Ok(permit) = permits.clone().try_acquire_owned() else { drop(socket); continue; };
                    let app = app.clone(); let mut stopping = stopping.clone();
                    connections.spawn(async move {
                        let _permit = permit;
                        let service = service_fn(move |req| { let app = app.clone(); async move { Ok::<_, Infallible>(app.handle(req).await) } });
                        let mut builder = http1::Builder::new();
                        builder.timer(hyper_util::rt::TokioTimer::new()).header_read_timeout(Duration::from_secs(5));
                        let conn = builder.serve_connection(TokioIo::new(socket), service); tokio::pin!(conn);
                        tokio::select! { _ = &mut conn => {}, _ = stopping.changed() => { conn.as_mut().graceful_shutdown(); let _ = conn.await; } }
                    });
                }
            }
        }
        drop(listener);
        let _ = stop.send(true);
        if tokio::time::timeout(Duration::from_secs(10), async {
            while connections.join_next().await.is_some() {}
        })
        .await
        .is_err()
        {
            connections.abort_all();
        }
        Ok(())
    }
    async fn handle(&self, req: Request<Incoming>) -> Response<Full<Bytes>> {
        let path = req.uri().path();
        if path == "/health" {
            return response(StatusCode::OK, json!({"ok":true,"status":"ready"}));
        }
        if req
            .headers()
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            != Some(format!("Bearer {}", self.environment.token).as_str())
        {
            return response(StatusCode::UNAUTHORIZED, json!({"error":"unauthorized"}));
        }
        // This implementation cannot enforce delegated-user/caller grants.
        // Fail closed rather than granting those callers installation authority.
        if req.headers().keys().any(|k| {
            k.as_str().starts_with("x-apteva-subject-")
                || k.as_str().starts_with("x-apteva-trusted-principal")
                || k.as_str().starts_with("x-apteva-bound-")
        }) {
            return response(
                StatusCode::FORBIDDEN,
                json!({"error":"caller-scoped tools are not supported"}),
            );
        }
        if path == "/manifest" && req.method() == Method::GET {
            return response(StatusCode::OK, self.manifest.clone());
        }
        if path != "/mcp" {
            return response(StatusCode::NOT_FOUND, json!({"error":"not found"}));
        }
        if req.method() != Method::POST {
            return response(StatusCode::METHOD_NOT_ALLOWED, json!({"error":"POST only"}));
        }
        let body = match tokio::time::timeout(
            Duration::from_secs(5),
            Limited::new(req.into_body(), 1 << 20).collect(),
        )
        .await
        {
            Ok(Ok(b)) => b.to_bytes(),
            Ok(Err(_)) => {
                return response(StatusCode::PAYLOAD_TOO_LARGE, json!({"error":"body limit"}))
            }
            Err(_) => {
                return response(StatusCode::REQUEST_TIMEOUT, json!({"error":"body timeout"}))
            }
        };
        let rpc: Value = match serde_json::from_slice(&body) {
            Ok(v) if matches!(v, Value::Object(_)) => v,
            _ => return rpc_error(Value::Null, -32700, "parse error"),
        };
        let id = rpc.get("id").cloned().unwrap_or(Value::Null);
        let result = match rpc["method"].as_str().unwrap_or("") {
            "initialize" => {
                json!({"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":false}},"serverInfo":{"name":self.manifest["name"],"version":self.manifest["version"]}})
            }
            "notifications/initialized" => json!({}),
            "tools/list" => {
                json!({"tools":self.tools.iter().map(|(name,t)|json!({"name":name,"description":t.description,"inputSchema":t.schema})).collect::<Vec<_>>()})
            }
            "tools/call" => {
                let Some(tool) = rpc["params"]["name"]
                    .as_str()
                    .and_then(|n| self.tools.get(n))
                else {
                    return rpc_error(id, -32602, "unknown tool");
                };
                let args = rpc["params"]
                    .get("arguments")
                    .cloned()
                    .unwrap_or_else(|| json!({}));
                if !args.is_object() {
                    return rpc_error(id, -32602, "arguments must be an object");
                }
                match (tool.handler)(&self.environment, args) {
                    Ok(v) => json!({"content":[{"type":"text","text":v.to_string()}]}),
                    Err(_) => return rpc_error(id, -32603, "tool failed"),
                }
            }
            _ => return rpc_error(id, -32601, "method not found"),
        };
        response(
            StatusCode::OK,
            json!({"jsonrpc":"2.0","id":id,"result":result}),
        )
    }
}
fn nonempty(v: &Value) -> bool {
    match v {
        Value::Null => false,
        Value::Array(a) => !a.is_empty(),
        Value::Object(o) => !o.is_empty(),
        Value::String(s) => !s.is_empty(),
        Value::Bool(b) => *b,
        _ => true,
    }
}
fn response(status: StatusCode, value: Value) -> Response<Full<Bytes>> {
    Response::builder()
        .status(status)
        .header("content-type", "application/json")
        .body(Full::new(Bytes::from(value.to_string())))
        .unwrap()
}
fn rpc_error(id: Value, code: i32, message: &str) -> Response<Full<Bytes>> {
    response(
        StatusCode::OK,
        json!({"jsonrpc":"2.0","id":id,"error":{"code":code,"message":message}}),
    )
}
async fn shutdown_signal() {
    #[cfg(unix)]
    {
        let mut term = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("SIGTERM handler");
        tokio::select! { _=term.recv()=>{}, _=tokio::signal::ctrl_c()=>{} }
    }
    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    // One environment test avoids parallel mutation of process-level settings.
    #[test]
    fn configuration_and_unsupported_contracts_fail_closed() {
        for key in [
            "APTEVA_DEPLOYMENT_ID",
            "APTEVA_DEPLOYMENT_GENERATION",
            "APTEVA_RUNTIME_MODE",
            "APTEVA_RESOURCE_POLICY",
            "APTEVA_APP_TOKEN",
            "APTEVA_APP_CONFIG",
            "APTEVA_APP_PORT",
            "APTEVA_BIND_HOST",
        ] {
            env::remove_var(key);
        }
        let raw = include_str!("../examples/hello/apteva.yaml");
        assert!(App::from_manifest_json(raw).is_err());
        env::set_var("APTEVA_APP_TOKEN", "unit-test-token");
        assert!(App::from_manifest_json(raw).is_ok());
        env::set_var("APTEVA_APP_CONFIG", "[1,2]");
        assert!(App::from_manifest_json(raw).is_err());
        env::set_var("APTEVA_APP_CONFIG", "{\"greeting\":\"Hi\"}");
        assert_eq!(
            App::from_manifest_json(raw).unwrap().environment.config["greeting"],
            "Hi"
        );
        for key in [
            "APTEVA_DEPLOYMENT_ID",
            "APTEVA_DEPLOYMENT_GENERATION",
            "APTEVA_RUNTIME_MODE",
            "APTEVA_RESOURCE_POLICY",
        ] {
            env::set_var(key, "unsupported");
            assert!(App::from_manifest_json(raw).is_err());
            env::remove_var(key);
        }
        env::set_var("APTEVA_APP_PORT", "0");
        assert!(App::from_manifest_json(raw).is_err());
        env::remove_var("APTEVA_APP_PORT");
        for (pointer, value) in [
            ("/components", json!({"worker":{}})),
            ("/db", json!({"migrations":"migrations"})),
            ("/runtime/resource_policy", json!({"defaults":{}})),
            ("/provides/permissions", json!([{"name":"write"}])),
            ("/provides/http_routes", json!([{"prefix":"/private"}])),
        ] {
            let mut manifest: Value = serde_json::from_str(raw).unwrap();
            let (parent, key) = pointer.rsplit_once('/').unwrap();
            manifest
                .pointer_mut(parent)
                .unwrap()
                .as_object_mut()
                .unwrap()
                .insert(key.into(), value);
            assert!(
                App::from_manifest_json(&manifest.to_string()).is_err(),
                "{pointer}"
            );
        }
        let mut manifest: Value = serde_json::from_str(raw).unwrap();
        manifest["provides"]["mcp_tools"][0]["requires"] = json!("write");
        assert!(App::from_manifest_json(&manifest.to_string()).is_err());
        env::remove_var("APTEVA_APP_TOKEN");
        env::remove_var("APTEVA_APP_CONFIG");
    }
}
