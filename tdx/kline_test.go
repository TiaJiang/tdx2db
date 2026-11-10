package tdx

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

func TestParseDayVolume(t *testing.T) {
	tests := []struct {
		name     string
		volRaw   uint32
		reserved uint32
		want     int64
	}{
		{"modern reserved", 189_000_000, 0x10000, 189_000_000},
		{"early sz reserved zero", 25_859_600, 0x00000000, 25_859_600},
		{"early sh reserved 1000", 174_085_000, 0x000003E8, 174_085_000},
		{"early sh reserved 3139", 40_631_800, 0x00000C43, 40_631_800},
		{"nonzero low byte", 478_000_000, 0x0000002A, 478_000_000},
		{"tdx hands marker", 47_846_559, 0xc364002f, 4_784_655_947},
		{"tdx hands marker exact", 47_846_560, 0xc3640000, 4_784_656_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDayVolume(tt.volRaw, tt.reserved)
			if got != tt.want {
				t.Errorf("parseDayVolume(%d, 0x%x) = %d, want %d", tt.volRaw, tt.reserved, got, tt.want)
			}
		})
	}
}

func TestProcessDayFileIgnoresReservedForVolume(t *testing.T) {
	data := make([]byte, recordSize)
	binary.LittleEndian.PutUint32(data[0:4], 19991110)
	binary.LittleEndian.PutUint32(data[4:8], 2775)
	binary.LittleEndian.PutUint32(data[8:12], 2775)
	binary.LittleEndian.PutUint32(data[12:16], 2775)
	binary.LittleEndian.PutUint32(data[16:20], 2775)
	binary.LittleEndian.PutUint32(data[20:24], math.Float32bits(float32(4_859_102_208)))
	binary.LittleEndian.PutUint32(data[24:28], 174_085_000)
	binary.LittleEndian.PutUint32(data[28:32], 0x000003E8)

	rows, err := processDayFile(data, "sh600000")
	if err != nil {
		t.Fatalf("processDayFile: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Volume != 174_085_000 {
		t.Errorf("volume = %d, want 174085000", rows[0].Volume)
	}
	if rows[0].Close != 27.75 {
		t.Errorf("close = %f, want 27.75", rows[0].Close)
	}
	if rows[0].UpCount != 0 || rows[0].DownCount != 0 {
		t.Errorf("breadth = (%d, %d), want (0, 0)", rows[0].UpCount, rows[0].DownCount)
	}
}

func TestProcessDayFileScalesC364VolumeMarker(t *testing.T) {
	data := make([]byte, recordSize)
	binary.LittleEndian.PutUint32(data[0:4], 20260312)
	binary.LittleEndian.PutUint32(data[4:8], 352)
	binary.LittleEndian.PutUint32(data[8:12], 380)
	binary.LittleEndian.PutUint32(data[12:16], 350)
	binary.LittleEndian.PutUint32(data[16:20], 380)
	binary.LittleEndian.PutUint32(data[20:24], math.Float32bits(float32(17_458_274_304)))
	binary.LittleEndian.PutUint32(data[24:28], 47_846_559)
	binary.LittleEndian.PutUint32(data[28:32], 0xc364002f)

	rows, err := processDayFile(data, "sh601868")
	if err != nil {
		t.Fatalf("processDayFile: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Volume != 4_784_655_947 {
		t.Errorf("volume = %d, want 4784655947", rows[0].Volume)
	}
	if rows[0].Close != 3.8 {
		t.Errorf("close = %f, want 3.8", rows[0].Close)
	}
}

func TestProcessDayFileParsesIndexBreadth(t *testing.T) {
	data := make([]byte, recordSize)
	binary.LittleEndian.PutUint32(data[0:4], 20260618)
	binary.LittleEndian.PutUint32(data[4:8], 292649)
	binary.LittleEndian.PutUint32(data[8:12], 293535)
	binary.LittleEndian.PutUint32(data[12:16], 291939)
	binary.LittleEndian.PutUint32(data[16:20], 292875)
	binary.LittleEndian.PutUint32(data[24:28], 123_456)
	binary.LittleEndian.PutUint32(data[28:32], 0x0026000c)

	rows, err := processDayFile(data, "sh000016")
	if err != nil {
		t.Fatalf("processDayFile: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Volume != 123_456 {
		t.Errorf("volume = %d, want 123456", rows[0].Volume)
	}
	if rows[0].UpCount != 12 || rows[0].DownCount != 38 {
		t.Errorf("breadth = (%d, %d), want (12, 38)", rows[0].UpCount, rows[0].DownCount)
	}
}

func TestProcessDayFileParsesBlockBreadth(t *testing.T) {
	data := make([]byte, recordSize)
	binary.LittleEndian.PutUint32(data[0:4], 20260618)
	binary.LittleEndian.PutUint32(data[24:28], 123_456)
	binary.LittleEndian.PutUint32(data[28:32], 0x003b0024)

	rows, err := processDayFile(data, "sh881044")
	if err != nil {
		t.Fatalf("processDayFile: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].UpCount != 36 || rows[0].DownCount != 59 {
		t.Errorf("breadth = (%d, %d), want (36, 59)", rows[0].UpCount, rows[0].DownCount)
	}
}

func TestProcessDayFileAfterFiltersOldRecords(t *testing.T) {
	data := make([]byte, recordSize*2)
	for i, day := range []uint32{20260102, 20260105} {
		offset := i * recordSize
		binary.LittleEndian.PutUint32(data[offset:offset+4], day)
		binary.LittleEndian.PutUint32(data[offset+4:offset+8], 1000)
		binary.LittleEndian.PutUint32(data[offset+8:offset+12], 1000)
		binary.LittleEndian.PutUint32(data[offset+12:offset+16], 1000)
		binary.LittleEndian.PutUint32(data[offset+16:offset+20], 1000)
	}

	rows, err := processDayFileAfter(data, "sh600000", time.Date(2026, 1, 2, 0, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatalf("processDayFileAfter: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if got := rows[0].Date.Format("2006-01-02"); got != "2026-01-05" {
		t.Fatalf("date = %s, want 2026-01-05", got)
	}
}

func TestProcessDayFileScalesConvertibleBond(t *testing.T) {
	data := make([]byte, recordSize)
	binary.LittleEndian.PutUint32(data[0:4], 20150306)
	binary.LittleEndian.PutUint32(data[4:8], 1_435_000)
	binary.LittleEndian.PutUint32(data[8:12], 1_483_000)
	binary.LittleEndian.PutUint32(data[12:16], 1_432_000)
	binary.LittleEndian.PutUint32(data[16:20], 1_459_300)

	rows, err := processDayFile(data, "sh113001")
	if err != nil {
		t.Fatalf("processDayFile: %v", err)
	}
	if rows[0].Close != 145.93 {
		t.Fatalf("close = %v, want 145.93", rows[0].Close)
	}
}
