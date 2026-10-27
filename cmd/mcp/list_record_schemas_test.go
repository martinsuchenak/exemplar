package mcp

import "testing"

func TestListRecordSchemasToolRegistered(t *testing.T) {
	if len(toolRegistrations) == 0 {
		t.Fatal("list_record_schemas tool should be registered via init()")
	}
}
