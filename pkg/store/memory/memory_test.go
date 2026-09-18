package memory

import (
	"testing"

	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/conformance"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) store.Backend {
		return New()
	})
}
