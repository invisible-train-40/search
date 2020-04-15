package search

import (
	"fmt"
)

var GetSearchMatchFactory func() SearchMatch
var GetMatchCollector MatchCollector
var GetBleveQueryFactory BleveQueryFactory
var GetIndexedFieldsMap IndexedFieldsMapFunc

func ValidateRegistry() error {
	if GetMatchCollector == nil {
		return fmt.Errorf("no match collector set, check that you set `search.GetMatchCollector`")
	}

	if GetBleveQueryFactory == nil {
		return fmt.Errorf("no bleve query factory set, check that you set `search.GetBleveQueryFactory`")
	}

	if GetSearchMatchFactory == nil {
		return fmt.Errorf("no search match factory set, check that you set `search.GetSearchMatchFactory`")
	}

	if GetIndexedFieldsMap == nil {
		return fmt.Errorf("no indexed fields map func set, check that you set `search.GetIndexedFieldsMap`")
	}

	return nil
}
