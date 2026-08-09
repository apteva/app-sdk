package sdk

import "testing"

func TestManifestAdditionalRuntimePort(t *testing.T) {
	m, err := ParseManifest([]byte(`
schema: apteva-app/v1
name: mqtt
display_name: MQTT
version: 1.0.0
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
  port: 8080
  ports:
    - name: mqtt
      container_port: 1883
      host_port: 1883
      protocol: tcp
`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(m.Runtime.Ports) != 1 || m.Runtime.Ports[0].ContainerPort != 1883 || m.Runtime.Ports[0].Protocol != "tcp" {
		t.Fatalf("runtime ports = %#v", m.Runtime.Ports)
	}
}

func TestManifestAdditionalRuntimePortValidation(t *testing.T) {
	for _, tc := range []struct {
		name, port string
	}{
		{"duplicate primary", "name: mqtt\n      container_port: 8080"},
		{"bad protocol", "name: mqtt\n      container_port: 1883\n      protocol: http"},
		{"bad name", "name: MQTT Broker\n      container_port: 1883"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(`
schema: apteva-app/v1
name: mqtt
display_name: MQTT
version: 1.0.0
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
  port: 8080
  ports:
    - ` + tc.port + "\n"))
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
