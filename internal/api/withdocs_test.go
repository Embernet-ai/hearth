package api

import (
	"context"
	"testing"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

type countingSpine struct {
	Spine
	updates int
}

func (c *countingSpine) Update(context.Context, string, core.Subjects, string, docs.UpdateParams) (*docs.Document, error) {
	c.updates++
	return &docs.Document{}, nil
}

// On the EmberOps back end the endpoints must write through the store that
// sends the change to EmberOps, not the Postgres service they were built with.
func TestWithDocsRoutesWritesThroughTheGivenStore(t *testing.T) {
	built, given := &countingSpine{}, &countingSpine{}
	s := New(nil, nil, built).WithDocs(given)
	if _, err := s.docs.Update(context.Background(), "o", core.Subjects{}, "d", docs.UpdateParams{}); err != nil {
		t.Fatal(err)
	}
	if built.updates != 0 || given.updates != 1 {
		t.Fatalf("the write went to the wrong store: built %d, given %d", built.updates, given.updates)
	}
}
