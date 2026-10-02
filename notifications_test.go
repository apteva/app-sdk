package sdk

import "testing"

func TestNotificationDeclarationValidation(t *testing.T) {
	manifest, err := ParseManifest([]byte(`
schema: apteva-app/v1
name: notify-test
version: 1.0.0
scopes: [project]
provides:
  publishes:
    - name: thing.created
      notification:
        id: thing-created
        name: Thing created
        audience: recipients
        recipients_field: recipient_user_ids
        title: "Created {title}"
        body: "Open {thing_id}"
        link: "?thing_id={thing_id}"
        group_by: thing_id
        defaults: {in_app: true, tab: true, desktop: false, mobile: false}
        filters:
          - {field: thing_id, label: Thing ID}
runtime:
  kind: source
  source: {repo: github.com/example/notify-test, ref: main, entry: .}
  port: 8080
`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if manifest.Provides.Publishes[0].Notification == nil {
		t.Fatal("notification metadata was dropped")
	}
}

func TestNotificationDeclarationRejectsOptOutDefaults(t *testing.T) {
	err := validateNotificationDeclarations([]EventDecl{{
		Name: "thing.created",
		Notification: &NotificationSpec{
			ID: "thing-created", Name: "Thing created", Audience: "project", Title: "Created",
			Defaults: NotificationChannels{Desktop: true},
		},
	}})
	if err == nil {
		t.Fatal("desktop notification default should require user opt-in")
	}
}
