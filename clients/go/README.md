# WenYuan Go client

A Go client for the WenYuan agent-memory HTTP API. The services and models under
this package are generated from the OpenAPI document this repository serves; the
module metadata, the `Client` wrapper and the tests are maintained by hand.

## Install

```
go get github.com/studyzy/wenyuan/clients/go
```

## Connect

`NewClient` reads the server base URL from the environment variable
`HINDSIGHT_API_URL` (default `http://localhost:18888`). Use one of the explicit
constructors to set the URL (and an optional Bearer token) in code:

```go
import (
	"context"

	wenyuan "github.com/studyzy/wenyuan/clients/go"
)

client := wenyuan.NewClient()                                   // HINDSIGHT_API_URL
client := wenyuan.NewClientWithBaseURL("http://localhost:18888")
client := wenyuan.NewClientWithToken("http://localhost:18888", "token")
client := wenyuan.NewClientWithTimeout("http://localhost:18888", "token", 30*time.Second)
```

The embedded `*wenyuan.APIClient` exposes every generated service (`MemoryAPI`,
`BanksAPI`, `DocumentsAPI`, `EntitiesAPI`, `MentalModelsAPI`, `WebhooksAPI`, …)
for anything beyond the core operations below.

## Retain, recall, reflect

```go
ctx := context.Background()
bankID := "team"

// Write memories.
_, err := client.Retain(ctx, bankID, wenyuan.RetainRequest{
	Items: []wenyuan.MemoryItem{
		{Content: wenyuan.TextContent("Alice joined the platform team")},
	},
})
if err != nil {
	log.Fatal(err)
}

// Read memories back.
recalled, err := client.Recall(ctx, bankID, *wenyuan.NewRecallRequest("who joined the platform team"))
if err != nil {
	log.Fatal(err)
}
for _, r := range recalled.Results {
	fmt.Println(r.Text)
}

// Synthesise an answer from the bank.
answer, err := client.Reflect(ctx, bankID, *wenyuan.NewReflectRequest("who joined the platform team?"))
if err != nil {
	log.Fatal(err)
}
fmt.Println(answer.Text)
```

Banks:

```go
create := wenyuan.CreateBankRequest{}
create.SetName("Team")
_, err := client.CreateBank(ctx, bankID, create)

config, err := client.GetBankConfig(ctx, bankID)
```

The profile read the contract retires (`GET .../profile`, `PUT .../profile`,
`POST .../background`) is generated — the document still carries those
operations, marked "removed" — but a server answers them with `410 Gone`, so
read configuration instead.

The convenience methods close the underlying HTTP response body before
returning. When you need the raw `*http.Response` or a service method not
wrapped here, call the generated API directly, e.g.
`client.MemoryAPI.ListMemories(ctx, bankID).Execute()`.

## Behaviour worth knowing

- **A bank's mission reads back as `reflect_mission`** in `GetBankConfig`, while
  the bank list carries it as `mission`. Both name the same setting; the
  configuration view uses the setting the mission feeds.
- **The retired profile read answers `410 Gone`.** Read configuration instead.
- **Requests identify the client** through `DefaultUserAgent`
  (`wenyuan-client-go/<module version>`, read from build info so it tracks the
  module version automatically). Assign `DefaultUserAgent` or set
  `cfg.UserAgent` to identify an integration instead — a caller that sets its
  own value replaces it rather than prefixing it.
- **A proxy is honoured through the standard library**: the generated client
  uses `http.DefaultTransport` unless you assign `cfg.HTTPClient`, so
  `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` behave the way they do everywhere else
  in Go.
- **Unknown response fields are ignored** and **null is distinct from absent**
  for optional fields (the generated `Nullable*` wrappers keep both states).

## Tests

Two suites, both in this package:

```
go test ./...                                     # offline: httptest servers only
HINDSIGHT_API_URL=http://127.0.0.1:18888 go test -tags integration ./...   # live server
```

From the repository root the same two are `make test-sdk-go` and
`make test-sdk-go-integration` (the latter reads `SDK_API_URL`, default
`http://127.0.0.1:18888`). A server started with
`HINDSIGHT_API_DATABASE_URL=memory` is enough for the live suite: it asserts
what a client can rely on (an operation succeeded, an id round-tripped, a list
carries what was written, a retired route answers 410, a tag filter selects the
write back out) rather than about extracted facts or synthesized prose, which
need a retention model.

The offline suite pins the two behaviours the generator needs patching for: a
union field serializes as its wire form (a bare string for text content), and
the decoder accepts fields it does not know.

## Regenerate

The generated surface is reproducible from a pinned generator. From the
repository root:

```
scripts/generate_clients.sh go
```

This rebuilds `clients/openapi.json` from the served document, runs
openapi-generator v7.10.0 (`-g go`) into `clients/go`, restores the maintained
files (this README, `go.mod`, the wrapper and the tests) and applies the small
post-generation fixes for known generator quirks. Regeneration never rewrites
the wrapper or the tests.