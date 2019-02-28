// Copyright 2018 The Fractal Team Authors
// This file is part of the fractal project.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package blockchain

import (
	"math/big"
	"testing"

	"github.com/fractalplatform/fractal/common"
	"github.com/fractalplatform/fractal/params"
	"github.com/fractalplatform/fractal/state"
	"github.com/fractalplatform/fractal/types"
	"github.com/fractalplatform/fractal/utils/fdb"
	"github.com/stretchr/testify/assert"
)

func TestForkController(t *testing.T) {
	var (
		testcfg    = &ForkConfig{ForkBlockNum: 10, Forkpercentage: 80}
		db         = fdb.NewMemDatabase()
		statedb, _ = state.New(common.Hash{}, state.NewDatabase(db))
	)

	fc := NewForkController(testcfg, params.DefaultChainconfig)
	var height int64
	for j := 0; j < 2; j++ {
		for i := 0; i < 8; i++ {
			block := &types.Block{Head: &types.Header{Number: big.NewInt(height)}}
			block.WithForkID(uint64(j), uint64(j+1))
			assert.NoError(t, fc.update(block, statedb))
			height++
		}

		for i := 0; i < 10; i++ {
			block := &types.Block{Head: &types.Header{Number: big.NewInt(height)}}
			block.WithForkID(uint64(j+1), uint64(j+1))
			assert.NoError(t, fc.update(block, statedb))
			height++
		}

		id, err := fc.currentForkID(statedb)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, uint64(j+1), id)
	}
}
