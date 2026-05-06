package policy

import "testing"

func TestParseFilenameStructuredComponents(t *testing.T) {
	components := map[string]ComponentRule{
		"MISSION_ID":      {Pattern: "CDM?", Length: 4, Charset: "ascii"},
		"FILE_TYPE":       {Length: 16, Charset: "ascii"},
		"START_TIME":      {Format: "YYYYMMDDTHHmmSSZ", Length: 16},
		"END_TIME":        {Format: "YYYYMMDDTHHmmSSZ", Length: 16},
		"GENERATION_TIME": {Format: "YYYYMMDDTHHmmSSZ", Length: 16},
	}
	pattern := EffectiveFilenamePattern(
		"<MISSION_ID>_<FILE_TYPE>_<START_TIME>_<END_TIME>_<GENERATION_TIME>_*",
		"",
		"PRIMARY_INPUT___",
	)
	got, err := ParseFilename("CDMA_PRIMARY_INPUT____20250703T110000Z_20250703T111500Z_20250703T113000Z_v2.txt", pattern, components)
	if err != nil {
		t.Fatalf("ParseFilename: %v", err)
	}
	if got["mission_id"] != "CDMA" {
		t.Fatalf("parsed components = %#v", got)
	}
	if got["start_time"] != "20250703T110000Z" || got["end_time"] != "20250703T111500Z" || got["generation_time"] != "20250703T113000Z" {
		t.Fatalf("parsed timestamps = %#v", got)
	}
}

func TestParseFilenameNoZTimestamps(t *testing.T) {
	components := map[string]ComponentRule{
		"MISSION_ID":      {Pattern: "CDM?", Length: 4, Charset: "ascii"},
		"FILE_TYPE":       {Length: 16, Charset: "ascii"},
		"START_TIME":      {Format: "YYYYMMDDTHHmmSS", Length: 15},
		"END_TIME":        {Format: "YYYYMMDDTHHmmSS", Length: 15},
		"GENERATION_TIME": {Format: "YYYYMMDDTHHmmSS", Length: 15},
	}
	pattern := EffectiveFilenamePattern(
		"<MISSION_ID>_<FILE_TYPE>_<START_TIME>_<END_TIME>_<GENERATION_TIME>_*",
		"",
		"PRIMARY_INPUT___",
	)
	got, err := ParseFilename("CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T113000_v2.txt", pattern, components)
	if err != nil {
		t.Fatalf("ParseFilename: %v", err)
	}
	if got["start_time"] != "20250703T110000" || got["end_time"] != "20250703T111500" || got["generation_time"] != "20250703T113000" {
		t.Fatalf("parsed timestamps = %#v", got)
	}
	// With-Z filename must NOT match a no-Z pattern
	if _, err := ParseFilename("CDMA_PRIMARY_INPUT____20250703T110000Z_20250703T111500Z_20250703T113000Z_v2.txt", pattern, components); err == nil {
		t.Fatal("with-Z filename should not match no-Z pattern")
	}
}

func TestParseFilenameRejectsWrongDeclaredFileType(t *testing.T) {
	components := map[string]ComponentRule{
		"MISSION_ID":      {Pattern: "CDM?", Length: 4, Charset: "ascii"},
		"FILE_TYPE":       {Length: 16, Charset: "ascii"},
		"START_TIME":      {Format: "YYYYMMDDTHHmmSSZ", Length: 16},
		"END_TIME":        {Format: "YYYYMMDDTHHmmSSZ", Length: 16},
		"GENERATION_TIME": {Format: "YYYYMMDDTHHmmSSZ", Length: 16},
	}
	pattern := EffectiveFilenamePattern("<MISSION_ID>_<FILE_TYPE>_<START_TIME>_<END_TIME>_<GENERATION_TIME>_*", "", "PRIMARY_INPUT___")
	if _, err := ParseFilename("CDMA_AUXILIARY_INPUT_20250703T110000Z_20250703T111500Z_20250703T113000Z_v2.txt", pattern, components); err == nil {
		t.Fatal("wrong declared file type matched structured pattern")
	}
}
