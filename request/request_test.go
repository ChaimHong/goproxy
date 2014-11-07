package request

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

//
// slugFromUrl
//

var slugFromUrlTests = []struct {
	in  string
	out string
}{
	{"/", ""},
	{"/foo", "foo"},
	{"/foo/bar", "foo"},
	{"foo", "foo"},
	{"/foo/bar/", "foo"},
	{"/foo-bar/fee/", "foo-bar"},
	{"/foo-bar/fee/", "foo-bar"},
}

func TestSlugFromUrl(t *testing.T) {
	for i, test := range slugFromUrlTests {
		actual := slugFromUrl(test.in)
		assert.Equal(t, test.out, actual, "Test %d", i+1)
	}
}
