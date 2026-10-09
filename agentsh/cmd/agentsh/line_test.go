package main

import (
	"encoding/json"
	"testing"

	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
)

func TestBuildMergeAndAttest(t *testing.T) {
	for _, c := range []struct {
		build func([]string) (agentrpc.Request, error)
		op    string
	}{{buildMerge, agentrpc.OpLineMerge}, {buildAttest, agentrpc.OpLineAttest}} {
		request, err := c.build([]string{"--rev", "feature"})
		if err != nil {
			t.Fatal(err)
		}
		{
			var params agentrpc.LineRequest
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if request.Op != c.op || params.Rev != "feature" {
				t.Fatalf("request = %+v params = %+v", request, params)
			}
		}
		defaulted, err := c.build(nil)
		if err != nil {
			t.Fatal(err)
		}
		{
			var params agentrpc.LineRequest
			if err := json.Unmarshal(defaulted.Params, &params); err != nil || params.Rev != "" {
				t.Fatalf("default rev = %q, %v", params.Rev, err)
			}
		}
	}
}

func TestBuildMergeRejectsPositionalArgs(t *testing.T) {
	if _, err := buildMerge([]string{"extra"}); err == nil {
		t.Fatal("accepted a positional argument")
	}
}
