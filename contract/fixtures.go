package contract

import (
	"embed"
	"encoding/json"
)

//go:embed testdata/*.json
var fixturesFS embed.FS

func BasicFixtures() []Fixture {
	b, _ := fixturesFS.ReadFile("testdata/basic.json")
	var f []Fixture
	if e := json.Unmarshal(b, &f); e != nil {
		panic(e)
	}
	return f
}
