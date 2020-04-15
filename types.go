// Copyright 2019 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package search

import (
	"github.com/dfuse-io/bstream"
	pbsearch "github.com/dfuse-io/pbgo/dfuse/search/v1"
)

type SearchMatch interface {
	BlockNum() uint64
	TransactionIDPrefix() string

	GetIndex() uint64
	SetIndex(index uint64)

	FillProtoSpecific(match *pbsearch.SearchMatch, blk *bstream.Block) error
}
