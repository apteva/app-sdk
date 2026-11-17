use apteva_app_sdk::{App, AppResult};
use serde_json::json;
#[tokio::main]
async fn main() -> AppResult<()> {
    App::from_manifest_json(include_str!("../apteva.yaml"))?
        .tool("hello", "A safe greeting for contract tests", json!({"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}), |env,args| {
            let name = args["name"].as_str().filter(|n|n.len()<=100).ok_or("invalid name")?;
            let prefix = env.config["greeting"].as_str().unwrap_or("Hello");
            Ok(json!({"message":format!("{prefix}, {name}!"),"install_id":env.install_id,"project_id":env.project_id}))
        })?
        .run().await
}
