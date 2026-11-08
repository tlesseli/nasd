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
