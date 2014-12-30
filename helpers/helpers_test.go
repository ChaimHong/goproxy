package helpers

import (
	"testing"

	"github.com/marbemac/stoplight/models"
	"github.com/stretchr/testify/assert"
)

//
// urlWithoutEnvironment
//

var urlWithoutEnvironmentTests = []struct {
	env *models.Environment
	in  string
	out string
}{
	{&models.Environment{Slug: "foo"}, "/foo", "/"},
	{&models.Environment{Slug: "foo"}, "/foo/bar", "/bar"},
	{&models.Environment{Slug: "foo"}, "foo", "/"},
	{&models.Environment{Slug: "foo"}, "foo/bar", "/bar"},
	{&models.Environment{Slug: "foo"}, "foo/bar/", "/bar/"},
}

func TestUrlWithoutEnvironment(t *testing.T) {
	for i, test := range urlWithoutEnvironmentTests {
		actual := urlWithoutEnvironment(test.env, test.in)
		assert.Equal(t, test.out, actual, "Test %d", i+1)
	}
}
