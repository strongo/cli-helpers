package cliinstall

// relevanceRow is one row of the Feature's relevance matrix table
// (cli-install#req:relevance-matrix). matrixRows is the single source of
// truth for every host -> target relevance text in the catalog; Relevant
// filters it by host, preserving this order, which is also listing order
// (cli-install#req:list-relevant).
//
// Every row here must equal a row of
// spec/features/cli-install/README.md's REQ: relevance-matrix table
// verbatim — TestCatalogMatrixEqualsFeatureTable reads that file and proves
// it. Text fixes are batched into one propagation wave per the plan's
// "Catalog text is frozen in task-2" approach note, not edited ad hoc here.
type relevanceRow struct {
	Host   string
	Target string
	Text   string
}

var matrixRows = []relevanceRow{
	{"wb", "specscore", "`wb`'s `ci` profile runs `specscore spec lint` for every repository with `spec/`"},
	{"wb", "codegrapher", "a `wb` lifecycle hook can run `codegrapher sync --init` whenever a checkout updates, keeping code indexes fresh"},
	{"wb", "cover100", "`wb coverage` measures many repositories; `cover100` opens one repository's coverage as a zoomable treemap"},

	{"specscore", "wb", "lint every synced repository's specs through `wb`'s CI profile instead of one clone at a time"},
	{"specscore", "ingitdb", "`specscore studio index` exports its facts as INGR recordsets, one of the record formats inGitDB stores natively; use `ingitdb` to keep your own structured project data in Git in the same format and check it with `ingitdb validate`"},
	{"specscore", "synchestra", "Synchestra turns the SpecScore features and plans you write into task queues AI agents claim and work from, tracking status in a separate state repository"},
	{"specscore", "chatwright", "verify conversational features with Chatwright scenarios alongside `specscore rehearse` acceptance scenarios"},
	{"specscore", "codegrapher", "CodeGrapher links source symbols to the SpecScore artifacts they implement"},

	{"chatwright", "specscore", "specify the conversational behavior your Chatwright scenarios prove as SpecScore features, and lint them with `specscore spec lint`"},

	{"codegrapher", "specscore", "lint and query the specs CodeGrapher's traceability edges point to"},
	{"codegrapher", "wb", "let `wb` re-run `codegrapher sync` automatically after it updates a checkout"},
	{"codegrapher", "cover100", "pair CodeGrapher's call graph with cover100's coverage treemap to find heavily called code that lacks tests"},

	{"cover100", "codegrapher", "from an uncovered file in the treemap, run `codegrapher callers` to see what calls into it"},
	{"cover100", "wb", "measure coverage across a whole local fleet with `wb coverage --fleet`, then open single repositories in cover100"},

	{"ovdb", "ingitdb", "inGitDB is one of OpenVaultDB's pluggable storage engines; `ingitdb` validates and edits that data directly"},
	{"ovdb", "datatug", "DataTug connects to a running `ovdb serve` database as an `openvaultdb` catalog with a scoped OpenVaultDB token, so you can query it and copy data out of it from DataTug's CLI and Web UI, under the server's own access policies"},

	{"synchestra", "ingitdb", "Synchestra uses inGitDB as its storage engine; `ingitdb` inspects and validates that state"},
	{"synchestra", "specscore", "lint and scaffold the SpecScore features and plans Synchestra coordinates"},
	{"synchestra", "datatug", "query and explore the inGitDB-stored project state in DataTug"},
	{"synchestra", "ovdb", "OpenVaultDB's default engine is inGitDB, the same engine Synchestra stores state in; `ovdb serve` puts an inGitDB database behind an HTTP API with scoped, revocable tokens for tools that do not work in Git"},
	{"synchestra", "chatwright", "prove a delivered conversational agent with deterministic Chatwright scenario runs"},

	{"ingitdb", "datatug", "explore and query inGitDB collections in DataTug's CLI and Web UI"},
	{"ingitdb", "ovdb", "serve an inGitDB repository as one storage engine behind OpenVaultDB"},
	{"ingitdb", "synchestra", "Synchestra coordinates AI agents from a Git state repository kept in inGitDB (tasks, claims, status) and bundles `ingitdb` pull/setup/resolve, so your inGitDB repositories and skills carry straight into agent coordination"},
	{"ingitdb", "specscore", "`specscore studio index` exports an ecosystem's spec, code-graph and manifest facts as INGR recordsets, a record format your inGitDB collections already support"},

	{"datatug", "ingitdb", "create, validate and edit the inGitDB databases DataTug reads"},
	{"datatug", "ovdb", "run a user-owned OpenVaultDB server with `ovdb serve`, then register it in DataTug as an `openvaultdb` catalog to query it under that server's access policies"},
	{"datatug", "specscore", "if you're contributing to or extending DataTug, its own specifications are SpecScore artifacts — read and lint them with `specscore spec lint`"},
}
