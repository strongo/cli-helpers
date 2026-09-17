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
	{"specscore", "ingitdb", "`specscore studio index` exports facts as INGR recordsets, inGitDB's record encoding"},
	{"specscore", "synchestra", "Synchestra is built on SpecScore and coordinates agents working from its features and plans"},
	{"specscore", "chatwright", "verify conversational features with Chatwright scenarios alongside `specscore rehearse` acceptance scenarios"},
	{"specscore", "codegrapher", "codegrapher links source symbols to the SpecScore artifacts they implement"},

	{"chatwright", "specscore", "Chatwright is developed spec-first with SpecScore; specify the behavior your scenarios prove"},

	{"codegrapher", "specscore", "lint and query the specs codegrapher's traceability edges point to"},
	{"codegrapher", "wb", "let `wb` re-run `codegrapher sync` automatically after it updates a checkout"},
	{"codegrapher", "cover100", "pair the dependency graph with a coverage treemap to find heavily used code that lacks tests"},

	{"cover100", "codegrapher", "from an uncovered file in the treemap, query codegrapher for what calls into it"},
	{"cover100", "wb", "measure coverage across a whole local fleet with `wb coverage --fleet`, then open single repositories in cover100"},

	{"ovdb", "ingitdb", "inGitDB is one of OpenVaultDB's pluggable storage engines; `ingitdb` validates and edits that data directly"},
	{"ovdb", "datatug", "DataTug queries inGitDB databases, so it can explore data an OpenVaultDB instance keeps on the inGitDB engine"},

	{"synchestra", "ingitdb", "Synchestra uses inGitDB as its storage engine; `ingitdb` inspects and validates that state"},
	{"synchestra", "specscore", "lint and scaffold the SpecScore features and plans Synchestra coordinates"},
	{"synchestra", "datatug", "query and explore the inGitDB-stored project state in DataTug"},
	{"synchestra", "ovdb", "keep data an agent task produces in a user-owned, portable OpenVaultDB database"},
	{"synchestra", "chatwright", "prove a delivered conversational agent with deterministic Chatwright scenario runs"},

	{"ingitdb", "datatug", "explore and query inGitDB collections in DataTug's CLI and Web UI"},
	{"ingitdb", "ovdb", "serve an inGitDB repository as one storage engine behind OpenVaultDB"},
	{"ingitdb", "synchestra", "Synchestra stores its coordination state in inGitDB"},
	{"ingitdb", "specscore", "SpecScore Studio exports spec facts as INGR recordsets you can keep beside inGitDB data"},

	{"datatug", "ingitdb", "create, validate and edit the inGitDB databases DataTug reads"},
	{"datatug", "ovdb", "run user-owned OpenVaultDB databases, including on the inGitDB engine DataTug reads"},
	{"datatug", "specscore", "DataTug's own specifications are SpecScore artifacts; read and lint them when contributing"},
}
