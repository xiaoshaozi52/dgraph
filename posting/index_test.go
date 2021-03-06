package posting

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dgraph-io/dgraph/protos/taskp"
	"github.com/dgraph-io/dgraph/protos/typesp"
	"github.com/dgraph-io/dgraph/schema"
	"github.com/dgraph-io/dgraph/store"
	"github.com/dgraph-io/dgraph/types"
	"github.com/dgraph-io/dgraph/x"
)

const schemaStr = `
name:string @index
`

func TestIndexingInt(t *testing.T) {
	schema.ParseBytes([]byte("age:int @index"), 1)
	a, err := IndexTokens("age", types.Val{types.StringID, []byte("10")})
	require.NoError(t, err)
	require.EqualValues(t, []byte{0x6, 0x1, 0x0, 0x0, 0x0, 0xa}, []byte(a[0]))
}

func TestIndexingIntNegative(t *testing.T) {
	schema.ParseBytes([]byte("age:int @index"), 1)
	a, err := IndexTokens("age", types.Val{types.StringID, []byte("-10")})
	require.NoError(t, err)
	require.EqualValues(t, []byte{0x6, 0x0, 0xff, 0xff, 0xff, 0xf6}, []byte(a[0]))
}

func TestIndexingFloat(t *testing.T) {
	schema.ParseBytes([]byte("age:float @index"), 1)
	a, err := IndexTokens("age", types.Val{types.StringID, []byte("10.43")})
	require.NoError(t, err)
	require.EqualValues(t, []byte{0x7, 0x1, 0x0, 0x0, 0x0, 0xa}, []byte(a[0]))
}

func TestIndexingDate(t *testing.T) {
	schema.ParseBytes([]byte("age:date @index"), 1)
	a, err := IndexTokens("age", types.Val{types.StringID, []byte("0010-01-01")})
	require.NoError(t, err)
	require.EqualValues(t, []byte{0x3, 0x1, 0x0, 0x0, 0x0, 0xa}, []byte(a[0]))
}

func TestIndexingTime(t *testing.T) {
	schema.ParseBytes([]byte("age:datetime @index"), 1)
	a, err := IndexTokens("age", types.Val{types.StringID, []byte("0010-01-01T01:01:01.000000001")})
	require.NoError(t, err)
	require.EqualValues(t, []byte{0x4, 0x1, 0x0, 0x0, 0x0, 0xa}, []byte(a[0]))
}

func TestIndexing(t *testing.T) {
	schema.ParseBytes([]byte("name:string @index"), 1)
	a, err := IndexTokens("name", types.Val{types.StringID, []byte("abc")})
	require.NoError(t, err)
	require.EqualValues(t, "\x01abc", string(a[0]))
}

func addMutationWithIndex(t *testing.T, l *List, edge *taskp.DirectedEdge, op uint32) {
	if op == Del {
		edge.Op = taskp.DirectedEdge_DEL
	} else if op == Set {
		edge.Op = taskp.DirectedEdge_SET
	} else {
		x.Fatalf("Unhandled op: %v", op)
	}
	require.NoError(t, l.AddMutationWithIndex(context.Background(), edge))
}

func TestTokensTable(t *testing.T) {
	dir, err := ioutil.TempDir("", "storetest_")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	schema.ParseBytes([]byte(schemaStr), 1)

	ps, err := store.NewStore(dir)
	defer ps.Close()
	require.NoError(t, err)
	Init(ps)

	key := x.DataKey("name", 1)
	l := getNew(key, ps)

	edge := &taskp.DirectedEdge{
		Value:  []byte("david"),
		Label:  "testing",
		Attr:   "name",
		Entity: 157,
	}
	addMutationWithIndex(t, l, edge, Set)

	key = x.IndexKey("name", "david")
	slice, err := ps.Get(key)
	require.NoError(t, err)

	var pl typesp.PostingList
	x.Check(pl.Unmarshal(slice.Data()))

	require.EqualValues(t, []string{"\x01david"}, tokensForTest("name"))

	CommitLists(10)
	time.Sleep(time.Second)

	slice, err = ps.Get(key)
	require.NoError(t, err)
	x.Check(pl.Unmarshal(slice.Data()))

	require.EqualValues(t, []string{"\x01david"}, tokensForTest("name"))
}

const schemaStrAlt = `
name:string @index
dob:date @index
`

// tokensForTest returns keys for a table. This is just for testing / debugging.
func tokensForTest(attr string) []string {
	pk := x.ParsedKey{Attr: attr}
	prefix := pk.IndexPrefix()
	it := pstore.NewIterator()
	defer it.Close()

	var out []string
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		k := x.Parse(it.Key().Data())
		x.AssertTrue(k.IsIndex())
		out = append(out, k.Term)
	}
	return out
}

// addEdgeToValue adds edge without indexing.
func addEdgeToValue(t *testing.T, ps *store.Store, attr string, src uint64,
	value string) {
	edge := &taskp.DirectedEdge{
		Value:  []byte(value),
		Label:  "testing",
		Attr:   attr,
		Entity: src,
		Op:     taskp.DirectedEdge_SET,
	}
	l, _ := GetOrCreate(x.DataKey(attr, src), 0)
	// No index entries added here as we do not call AddMutationWithIndex.
	ok, err := l.AddMutation(context.Background(), edge)
	require.NoError(t, err)
	require.True(t, ok)
}

func populateGraph(t *testing.T) (string, *store.Store) {
	dir, err := ioutil.TempDir("", "storetest_")
	require.NoError(t, err)

	ps, err := store.NewStore(dir)
	require.NoError(t, err)

	schema.ParseBytes([]byte(schemaStrAlt), 1)
	Init(ps)

	addEdgeToValue(t, ps, "name", 1, "Michonne")
	addEdgeToValue(t, ps, "name", 20, "David")
	return dir, ps
}

func TestRebuildIndex(t *testing.T) {
	dir, ps := populateGraph(t)
	defer ps.Close()
	defer os.RemoveAll(dir)

	// RebuildIndex requires the data to be committed to data store.
	CommitLists(10)
	for len(syncCh) > 0 {
		time.Sleep(100 * time.Millisecond)
	}

	// Create some fake wrong entries for data store.
	ps.SetOne(x.IndexKey("name", "wrongname1"), []byte("nothing"))
	ps.SetOne(x.IndexKey("name", "wrongname2"), []byte("nothing"))

	require.NoError(t, RebuildIndex(context.Background(), "name"))

	// Let's force a commit.
	CommitLists(10)
	for len(syncCh) > 0 {
		time.Sleep(100 * time.Millisecond)
	}

	// Check index entries in data store.
	it := ps.NewIterator()
	defer it.Close()
	pk := x.ParsedKey{Attr: "name"}
	prefix := pk.IndexPrefix()
	var idxKeys []string
	var idxVals []*typesp.PostingList
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		idxKeys = append(idxKeys, string(it.Key().Data()))
		pl := new(typesp.PostingList)
		require.NoError(t, pl.Unmarshal(it.Value().Data()))
		idxVals = append(idxVals, pl)
	}
	require.Len(t, idxKeys, 2)
	require.Len(t, idxVals, 2)
	require.EqualValues(t, x.IndexKey("name", "\x01david"), idxKeys[0])
	require.EqualValues(t, x.IndexKey("name", "\x01michonne"), idxKeys[1])
	require.Len(t, idxVals[0].Postings, 1)
	require.Len(t, idxVals[1].Postings, 1)
	require.EqualValues(t, idxVals[0].Postings[0].Uid, 20)
	require.EqualValues(t, idxVals[1].Postings[0].Uid, 1)
}
