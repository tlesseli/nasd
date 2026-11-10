package disks

import (
	"context"
	"testing"
)

// Real lsblk output captured from the dev VM (trimmed): virtio system disk
// with partitions, two SATA data disks, one of them already in a RAID.
const lsblkSample = `{"blockdevices": [
  {"name":"sda","path":"/dev/sda","size":2147483648,"model":"DEV-DISK-1","serial":"NASDISK0001","type":"disk","tran":"sata","rota":true,"fstype":"linux_raid_member","mountpoints":[null],
   "children":[{"name":"md0","path":"/dev/md0","size":4290772992,"model":null,"serial":null,"type":"raid5","tran":null,"rota":true,"fstype":"ext4","mountpoints":["/data"]}]},
  {"name":"sdb","path":"/dev/sdb","size":"2147483648","model":"DEV-DISK-2","serial":"NASDISK0002","type":"disk","tran":"sata","rota":"0","fstype":null,"mountpoints":[null]},
  {"name":"vdb","path":"/dev/vdb","size":374784,"type":"disk","ro":true,"mountpoints":[null]},
  {"name":"sr0","path":"/dev/sr0","size":1024,"type":"rom","mountpoints":[null]},
  {"name":"vda","path":"/dev/vda","size":8589934592,"model":null,"serial":null,"type":"disk","tran":null,"rota":true,"fstype":null,"mountpoints":[null],
   "children":[{"name":"vda1","path":"/dev/vda1","size":8476688896,"type":"part","fstype":"ext4","mountpoints":["/"]},
               {"name":"vda15","path":"/dev/vda15","size":133169152,"type":"part","fstype":"vfat","mountpoints":["/boot/efi"]}]}
]}`

func TestParseLsblk(t *testing.T) {
	disks, err := parseLsblk([]byte(lsblkSample))
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 3 {
		t.Fatalf("want 3 disks (sr0 excluded), got %d", len(disks))
	}
	sda, sdb, vda := disks[0], disks[1], disks[2]
	if sda.Usage != "raid member" || sda.Role != "data" || !sda.Rotational {
		t.Errorf("sda: %+v", sda)
	}
	if sdb.SizeBytes != 2147483648 || sdb.Rotational || sdb.Usage != "" {
		t.Errorf("sdb (string size, \"0\" rota): %+v", sdb)
	}
	if vda.Role != "system" || vda.Usage != "ext4" {
		t.Errorf("vda: %+v", vda)
	}
}

func TestParseSmart(t *testing.T) {
	h := parseSmart([]byte(`{"smart_status":{"passed":false},"temperature":{"current":41},"power_on_time":{"hours":31000},
	  "ata_smart_attributes":{"table":[{"id":5,"raw":{"value":12}},{"id":197,"raw":{"value":3}},{"id":198,"raw":{"value":1}}]}}`))
	if !h.Supported || h.Passed || h.TemperatureC != 41 || h.PowerOnHours != 31000 ||
		h.Reallocated != 12 || h.Pending != 3 || h.Uncorrectable != 1 {
		t.Errorf("%+v", h)
	}

	h = parseSmart([]byte(`{"smartctl":{"messages":[{"string":"/dev/vda: Unable to detect device type","severity":"error"}]}}`))
	if h.Supported || h.Error == "" {
		t.Errorf("unsupported device: %+v", h)
	}
}

func TestParseSelfTests(t *testing.T) {
	// A long test running (60% left), after an extended test that failed
	// on a read error and older short tests.
	h := parseSmart([]byte(`{"smart_status":{"passed":true},
	  "ata_smart_data":{"self_test":{"status":{"value":246},"polling_minutes":{"short":1,"extended":222}}},
	  "ata_smart_self_test_log":{"standard":{"table":[
	    {"type":{"value":2},"status":{"value":249,"string":"Self-test routine in progress"},"lifetime_hours":900},
	    {"type":{"value":1},"status":{"value":0,"string":"Completed without error"},"lifetime_hours":800},
	    {"type":{"value":2},"status":{"value":121,"string":"Completed: read failure"},"lifetime_hours":700},
	    {"type":{"value":2},"status":{"value":0,"string":"Completed without error"},"lifetime_hours":600}]}}}`))
	st := h.SelfTest
	if st == nil || !st.Running || st.RemainingPct != 60 || st.LongMinutes != 222 {
		t.Fatalf("%+v", st)
	}
	if l := st.LastLong; l == nil || l.Hours != 700 || l.Passed || !l.Failed {
		t.Errorf("last long test: %+v", l)
	}

	// Aborted by the host: neither passed nor failed.
	h = parseSmart([]byte(`{"smart_status":{"passed":true},"ata_smart_self_test_log":{"standard":{"table":[
	    {"type":{"value":2},"status":{"value":16,"string":"Aborted by host"},"lifetime_hours":50}]}}}`))
	if l := h.SelfTest.LastLong; l == nil || l.Passed || l.Failed || h.SelfTest.Running {
		t.Errorf("aborted: %+v", h.SelfTest)
	}
}

func TestHealthIsCached(t *testing.T) {
	calls := 0
	s := NewService(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return []byte(`{"smart_status":{"passed":true}}`), nil
	})
	s.healthOf(context.Background(), "/dev/sda")
	s.healthOf(context.Background(), "/dev/sda")
	if calls != 1 {
		t.Errorf("smartctl called %d times, want 1", calls)
	}
}

func TestSleepingDiskKeepsLastHealth(t *testing.T) {
	asleep := false
	s := NewService(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if asleep {
			return []byte(`{"smartctl":{"messages":[{"string":"Device is in STANDBY mode, exit(2)","severity":"information"}],"exit_status":2}}`), nil
		}
		return []byte(`{"smart_status":{"passed":true},"temperature":{"current":33}}`), nil
	})
	s.healthTTL = 0
	h := s.healthOf(context.Background(), "/dev/sda")
	asleep = true
	h2 := s.healthOf(context.Background(), "/dev/sda")
	if !h2.Standby || !h2.Supported || !h2.Passed || h2.TemperatureC != 33 || h.Standby {
		t.Errorf("asleep disk should report its last health: %+v", h2)
	}
}
