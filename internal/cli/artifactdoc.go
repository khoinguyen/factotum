package cli

import (
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

// artifactDoc is the stable, snake_case structured shape shared by the artifact
// verbs (ft doc get/list and ft memory get/create). It is the machine-readable
// interface, so core entity field names never leak into output.
//
// Fields are omitempty so each verb can project the subset it wants: doc list
// omits the body, and memory get omits kind and path to keep its established
// shape (see memoryDocFrom).
type artifactDoc struct {
	ID        string    `json:"id" yaml:"id"`
	ProjectID string    `json:"project_id" yaml:"project_id"`
	TaskID    string    `json:"task_id,omitempty" yaml:"task_id,omitempty"`
	Kind      string    `json:"kind,omitempty" yaml:"kind,omitempty"`
	Title     string    `json:"title" yaml:"title"`
	Brief     string    `json:"brief,omitempty" yaml:"brief,omitempty"`
	Path      string    `json:"path,omitempty" yaml:"path,omitempty"`
	Body      string    `json:"body,omitempty" yaml:"body,omitempty"`
	Links     []linkDoc `json:"links,omitempty" yaml:"links,omitempty"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}

func artifactDocFrom(artifact *core.Artifact) artifactDoc {
	doc := artifactDoc{
		ID:        string(artifact.ID),
		ProjectID: string(artifact.ProjectID),
		Kind:      string(artifact.Kind),
		Title:     artifact.Title,
		Brief:     artifact.Brief,
		Path:      artifact.Path,
		Body:      artifact.Body,
		CreatedAt: artifact.CreatedAt,
		UpdatedAt: artifact.UpdatedAt,
	}
	if artifact.TaskID != nil {
		doc.TaskID = string(*artifact.TaskID)
	}
	for _, link := range artifact.Links {
		doc.Links = append(doc.Links, linkDoc{Kind: string(link.Kind), URL: link.URL, Title: link.Title})
	}
	return doc
}

// memoryDocFrom is artifactDocFrom with kind and path dropped, so the memory
// verbs keep their long-standing output shape (a memory is identified by its
// project, not its kind).
func memoryDocFrom(artifact *core.Artifact) artifactDoc {
	doc := artifactDocFrom(artifact)
	doc.Kind = ""
	doc.Path = ""
	return doc
}
