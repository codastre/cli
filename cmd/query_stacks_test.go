package cmd

import (
	"reflect"
	"testing"
)

// resetQueryFilterFlags clears the package-level flag vars the two builders
// read, so one test's filter cannot leak into the next.
func resetQueryFilterFlags(t *testing.T) {
	t.Helper()
	prev := struct {
		topK         int
		ref          string
		language     string
		pathPrefix   string
		contentKinds []string
		stacks       []string
		alertIDs     []string
		errorCodes   []string
		corporaTopK  int
		corporaLang  string
		corporaKinds []string
		corporaStks  []string
	}{
		queryTopK, queryRef, queryLanguage, queryPathPrefix, queryContentKinds,
		queryStacks, queryAlertIDs, queryErrorCodes,
		corporaTopK, corporaLanguage, corporaContentKinds, corporaStacks,
	}
	t.Cleanup(func() {
		queryTopK, queryRef, queryLanguage, queryPathPrefix = prev.topK, prev.ref, prev.language, prev.pathPrefix
		queryContentKinds, queryStacks = prev.contentKinds, prev.stacks
		queryAlertIDs, queryErrorCodes = prev.alertIDs, prev.errorCodes
		corporaTopK, corporaLanguage = prev.corporaTopK, prev.corporaLang
		corporaContentKinds, corporaStacks = prev.corporaKinds, prev.corporaStks
	})
	queryTopK, queryRef, queryLanguage, queryPathPrefix = 6, "", "", ""
	queryContentKinds, queryStacks, queryAlertIDs, queryErrorCodes = nil, nil, nil, nil
	corporaTopK, corporaLanguage = 10, ""
	corporaContentKinds, corporaStacks = nil, nil
}

func TestQueryToolArgs_StacksUsesCanonicalWireName(t *testing.T) {
	resetQueryFilterFlags(t)
	queryStacks = []string{"backend", "mobile"}

	args := queryToolArgs("payment webhook", formatHuman)

	got, ok := args["stacks"].([]string)
	if !ok {
		t.Fatalf("stacks missing or wrong type: %#v", args["stacks"])
	}
	if !reflect.DeepEqual(got, []string{"backend", "mobile"}) {
		t.Fatalf("stacks = %v, want [backend mobile]", got)
	}
	// The singular and a query-text prefix are both wrong; only "stacks" is wire.
	if _, bad := args["stack"]; bad {
		t.Fatal("sent a 'stack' key: the wire parameter is 'stacks'")
	}
}

func TestQueryToolArgs_PassesAliasesThroughVerbatim(t *testing.T) {
	resetQueryFilterFlags(t)
	queryStacks = []string{"android", "ios"}

	args := queryToolArgs("push token refresh", formatHuman)

	// The server owns normalization. If the CLI expanded these itself it could
	// disagree with a tenant that relabeled its catalog.
	if got := args["stacks"].([]string); !reflect.DeepEqual(got, []string{"android", "ios"}) {
		t.Fatalf("stacks = %v, want the aliases unchanged", got)
	}
}

func TestQueryToolArgs_NoStackFlagSendsNoFilter(t *testing.T) {
	resetQueryFilterFlags(t)

	args := queryToolArgs("auth middleware", formatHuman)

	if _, present := args["stacks"]; present {
		t.Fatal("sent a stacks key with no --stacks flag: an empty list is a filter")
	}
}

func TestCorporaToolArgs_SerialisesStacks(t *testing.T) {
	resetQueryFilterFlags(t)
	corporaStacks = []string{"backend", "mobile"}

	args := corporaToolArgs("customers report a declined card")

	if got := args["stacks"].([]string); !reflect.DeepEqual(got, []string{"backend", "mobile"}) {
		t.Fatalf("stacks = %v, want [backend mobile]", got)
	}
}

func TestCorporaToolArgs_NoStackFlagSendsNoFilter(t *testing.T) {
	resetQueryFilterFlags(t)

	args := corporaToolArgs("public api documentation")

	if _, present := args["stacks"]; present {
		t.Fatal("sent a stacks key with no --stacks flag")
	}
}

// --corpora forwards the shared flags to the corpus path; --stacks must ride
// along, since a corpus answer is exactly where a stack restriction pays off.
func TestCorporaFlagConflicts_StacksIsAllowed(t *testing.T) {
	changed := func(name string) bool { return name == "stacks" }

	errs, warns := corporaFlagConflicts(changed)

	if len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("--stacks rejected with --corpora: errs=%v warns=%v", errs, warns)
	}
}
