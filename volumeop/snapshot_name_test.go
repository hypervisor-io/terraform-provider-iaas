package volumeop

import (
	"context"
	"testing"
)

func TestCheckSnapshotNameFree_RejectsRewrittenNamesBeforeRead(t *testing.T) {
	for _, name := range []string{"", "   ", " nightly ", "nightly\t", "\nnightly", "0"} {
		t.Run(name, func(t *testing.T) {
			sv := &snapVolume{}
			before, err := CheckSnapshotNameFree(context.Background(), sv.server(t), "vol-1", name)
			if err == nil || len(before) != 0 {
				t.Fatalf("name=%q before=%v err=%v; want rejection without ids", name, before, err)
			}
			if sv.gets != 0 {
				t.Fatalf("name=%q reads=%d; reject before any request", name, sv.gets)
			}
		})
	}
}

func TestCheckSnapshotNameFree_PreservesRoundTripNames(t *testing.T) {
	for _, name := range []string{"nightly", "00", "nightly copy"} {
		t.Run(name, func(t *testing.T) {
			sv := &snapVolume{snaps: [][2]string{{"foreign", "other"}}}
			before, err := CheckSnapshotNameFree(context.Background(), sv.server(t), "vol-1", name)
			if err != nil || len(before) != 0 || sv.gets != 1 {
				t.Fatalf("name=%q before=%v err=%v reads=%d; want an unchanged free name", name, before, err, sv.gets)
			}
			sv.snaps = append(sv.snaps, [2]string{"existing", name})
			before, err = CheckSnapshotNameFree(context.Background(), sv.server(t), "vol-1", name)
			if err == nil || len(before) != 1 || before[0] != "existing" {
				t.Fatalf("name=%q before=%v err=%v; exact configured identity must still detect collisions", name, before, err)
			}
		})
	}
}
