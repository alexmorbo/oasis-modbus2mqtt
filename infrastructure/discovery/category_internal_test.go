package discovery

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
)

func TestCategoryString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		category catalog.EntityCategory
		want     string
	}{
		{"default", catalog.EntityCategoryDefault, ""},
		{"diagnostic", catalog.EntityCategoryDiagnostic, "diagnostic"},
		{"config", catalog.EntityCategoryConfig, "config"},
		{"unknown_value", catalog.EntityCategory(99), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, categoryString(tc.category))
		})
	}
}
