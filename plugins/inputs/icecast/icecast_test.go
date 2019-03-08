package icecast

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/influxdata/telegraf/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var icecastStatus = `
<?xml version="1.0" encoding="UTF-8"?>
<icestats><source mount="/mount.aac"><fallback/><listeners>420</listeners><Connected>806794</Connected><content-type>audio/aacp</content-type></source></icestats>
`

func TestHTTPicecast(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, icecastStatus)
	}))
	defer ts.Close()

	// Fetch it 2 times to catch possible data races.
	testURLs := make([][]string, 2)
	testURLs[0] = []string{ts.URL}
	testURLs[1] = []string{ts.URL}
	a := Icecast{
		URLs: testURLs,
	}

	var acc testutil.Accumulator
	require.NoError(t, acc.GatherError(a.Gather))

	fmt.Println(acc.TagValue("icecast", "host"))
	assert.True(t, acc.HasField("icecast", "listeners"))
	assert.True(t, acc.TagValue("icecast", "host") == "127.0.0.1")
	assert.True(t, acc.TagValue("icecast", "mount") == "mount.aac")
}

func TestHTTPicecastAlias(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, icecastStatus)
	}))
	defer ts.Close()

	// Fetch it 2 times to catch possible data races.
	testURLs := make([][]string, 2)
	testURLs[0] = []string{ts.URL, "alias"}
	testURLs[1] = []string{ts.URL, "alias"}
	a := Icecast{
		URLs: testURLs,
	}

	var acc testutil.Accumulator
	require.NoError(t, acc.GatherError(a.Gather))

	fmt.Println(acc.TagValue("icecast", "host"))
	assert.True(t, acc.HasField("icecast", "listeners"))
	assert.True(t, acc.TagValue("icecast", "host") == "alias")
	assert.True(t, acc.TagValue("icecast", "mount") == "mount.aac")
}
