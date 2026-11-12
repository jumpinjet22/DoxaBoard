// Package all registers every in-tree content connector. Import it for its
// side effects:
//
//	import _ "github.com/jumpinjet22/doxaboard/connectors/all"
//
// New in-tree connectors add a blank import here.
package all

import (
	_ "github.com/jumpinjet22/doxaboard/connectors/folder"
	_ "github.com/jumpinjet22/doxaboard/connectors/jsonfeed"
	_ "github.com/jumpinjet22/doxaboard/connectors/rss"
)
