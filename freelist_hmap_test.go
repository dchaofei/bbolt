package bbolt

import (
	"fmt"
	"reflect"
	"testing"
	"unsafe"
)

func TestA(t *testing.T) {
	const unalignedMask = unsafe.Alignof(struct {
		bucket
		page
	}{})
	fmt.Println(unalignedMask)
	fmt.Printf("%b\n\n", unalignedMask)
	fmt.Println(unalignedMask - 1)
	fmt.Printf("%b\n", unalignedMask-1)
}

func Test_freelist_Init(t *testing.T) {
	type fields struct {
		freelistType   FreelistType
		ids            []pgid
		allocs         map[pgid]txid
		pending        map[txid]*txPending
		cache          map[pgid]bool
		freemaps       map[uint64]pidSet
		forwardMap     map[pgid]uint64
		backwardMap    map[pgid]uint64
		allocate       func(txid txid, n int) pgid
		free_count     func() int
		mergeSpans     func(ids pgids)
		getFreePageIDs func() []pgid
		readIDs        func(pgids []pgid)
	}
	type args struct {
		pgids []pgid
	}
	type wants struct {
		freemaps    map[uint64]pidSet
		forwardMap  map[pgid]uint64
		backwardMap map[pgid]uint64
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		wants  wants
	}{
		{
			name:   "尾部不是连续页",
			fields: fields{},
			args:   args{pgids: []pgid{1, 2, 4, 5, 7, 8, 10, 12, 13, 14, 18}},
			wants: wants{
				freemaps: map[uint64]pidSet{
					2: {
						1: {},
						4: {},
						7: {},
					},
					1: {
						10: {},
						18: {},
					},
					3: {
						12: {},
					},
				},
				forwardMap: map[pgid]uint64{
					1:  2,
					4:  2,
					7:  2,
					10: 1,
					12: 3,
					18: 1,
				},
				backwardMap: map[pgid]uint64{
					2:  2,
					5:  2,
					8:  2,
					10: 1,
					14: 3,
					18: 1,
				},
			},
		},
		{
			name:   "尾部是连续页",
			fields: fields{},
			args:   args{pgids: []pgid{1, 2, 4, 5, 7, 8, 10, 12, 13, 14, 18, 19}},
			wants: wants{
				freemaps: map[uint64]pidSet{
					2: {
						1:  {},
						4:  {},
						7:  {},
						18: {},
					},
					1: {
						10: {},
					},
					3: {
						12: {},
					},
				},
				forwardMap: map[pgid]uint64{
					1:  2,
					4:  2,
					7:  2,
					10: 1,
					12: 3,
					18: 2,
				},
				backwardMap: map[pgid]uint64{
					2:  2,
					5:  2,
					8:  2,
					10: 1,
					14: 3,
					19: 2,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &freelist{
				freelistType:   tt.fields.freelistType,
				ids:            tt.fields.ids,
				allocs:         tt.fields.allocs,
				pending:        tt.fields.pending,
				cache:          tt.fields.cache,
				freemaps:       tt.fields.freemaps,
				forwardMap:     tt.fields.forwardMap,
				backwardMap:    tt.fields.backwardMap,
				allocate:       tt.fields.allocate,
				free_count:     tt.fields.free_count,
				mergeSpans:     tt.fields.mergeSpans,
				getFreePageIDs: tt.fields.getFreePageIDs,
				readIDs:        tt.fields.readIDs,
			}
			f.init(tt.args.pgids)
			if !reflect.DeepEqual(f.freemaps, tt.wants.freemaps) {
				t.Errorf("freemaps 不相等, got:[%+v], want:[%+v]", f.freemaps, tt.wants.freemaps)
			}
			if !reflect.DeepEqual(f.forwardMap, tt.wants.forwardMap) {
				t.Errorf("forwardMap 不相等, got:[%+v], want:[%+v]", f.forwardMap, tt.wants.forwardMap)
			}
			if !reflect.DeepEqual(f.backwardMap, tt.wants.backwardMap) {
				t.Errorf("backwardMap 不相等, got:[%+v], want:[%+v]", f.backwardMap, tt.wants.backwardMap)
			}
		})
	}
}
