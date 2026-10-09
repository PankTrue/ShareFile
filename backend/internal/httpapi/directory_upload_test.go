package httpapi

import (
	"encoding/json"
	"sharefile/backend/internal/metadata"
	"testing"
)

func TestFolderCreationIdempotencyAndIsolation(t *testing.T) {
	f := setup(t)
	headers := browser(t, f, f.alice)
	headers["Idempotency-Key"] = "persisted-folder-request"
	input := map[string]string{"parent_id": f.alice.HomeID, "name": "Папка"}
	create := func() metadata.Node {
		response := request(t, f.a.Public(), "POST", "/api/v1/folders", input, headers)
		status(t, response, 201)
		var node metadata.Node
		must(t, json.Unmarshal(response.Body.Bytes(), &node))
		return node
	}
	first := create()
	if retried := create(); retried.ID != first.ID {
		t.Fatal("lost response created a second folder")
	}
	input["name"] = "Другая папка"
	status(t, request(t, f.a.Public(), "POST", "/api/v1/folders", input, headers), 409)
	input["name"] = "Папка"
	headers["Idempotency-Key"] = "new-folder-request"
	second := create()
	if second.ID == first.ID || second.Name == first.Name {
		t.Fatal("independent selections merged into an existing folder")
	}
	bob := browser(t, f, f.bob)
	bob["Idempotency-Key"] = "persisted-folder-request"
	input["parent_id"] = f.bob.HomeID
	response := request(t, f.a.Public(), "POST", "/api/v1/folders", input, bob)
	status(t, response, 201)
	input["parent_id"] = f.alice.HomeID
	status(t, request(t, f.a.Public(), "POST", "/api/v1/folders", input, bob), 403)
	must(t, f.a.Files.Move(f.ctx, f.alice.ID, first.ID, second.ID, first.Name))
	headers["Idempotency-Key"] = "persisted-folder-request"
	status(t, request(t, f.a.Public(), "POST", "/api/v1/folders", input, headers), 409)
	must(t, f.a.Files.Move(f.ctx, f.alice.ID, first.ID, f.alice.HomeID, first.Name))
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, first.ID))
	headers["Idempotency-Key"] = "persisted-folder-request"
	status(t, request(t, f.a.Public(), "POST", "/api/v1/folders", input, headers), 409)
}
