package git

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
)

var ErrOutputMalformed = errors.New("malformed Git output")

type porcelainEntry struct {
	RecordType   byte
	XY           string
	Path         string
	OriginalPath string
}

type indexStage struct {
	Path  string
	Mode  string
	OID   string
	Stage int
}

type nameStatus struct {
	Status  byte
	Score   int
	OldPath string
	Path    string
}

type attributeRecord struct {
	Path  string
	Name  string
	Value string
}

type checkoutTemp struct {
	TempPath string
	Path     string
}

func splitNULTerminated(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != 0 {
		return nil, fmt.Errorf("%w: output is not NUL terminated", ErrOutputMalformed)
	}
	return bytes.Split(data[:len(data)-1], []byte{0}), nil
}

func parsePorcelainV2Z(data []byte) ([]porcelainEntry, error) {
	parts, err := splitNULTerminated(data)
	if err != nil {
		return nil, err
	}
	entries := make([]porcelainEntry, 0, len(parts))
	for index := 0; index < len(parts); index++ {
		record := parts[index]
		if len(record) < 3 || record[1] != ' ' {
			return nil, malformedConflictOutput("invalid porcelain record")
		}
		switch record[0] {
		case '1':
			fields := bytes.SplitN(record, []byte{' '}, 9)
			if len(fields) != 9 || !validPorcelainFields(fields, 6, 7) {
				return nil, malformedConflictOutput("invalid ordinary porcelain record")
			}
			entries = append(entries, porcelainEntry{RecordType: '1', XY: string(fields[1]), Path: string(fields[8])})
		case '2':
			fields := bytes.SplitN(record, []byte{' '}, 10)
			if len(fields) != 10 || index+1 >= len(parts) || len(parts[index+1]) == 0 || !validPorcelainFields(fields, 6, 7) || !validRenameScore(fields[8]) {
				return nil, malformedConflictOutput("invalid renamed porcelain record")
			}
			entries = append(entries, porcelainEntry{RecordType: '2', XY: string(fields[1]), Path: string(fields[9]), OriginalPath: string(parts[index+1])})
			index++
		case 'u':
			fields := bytes.SplitN(record, []byte{' '}, 11)
			if len(fields) != 11 || !validPorcelainFields(fields, 7, 8, 9) {
				return nil, malformedConflictOutput("invalid unmerged porcelain record")
			}
			entries = append(entries, porcelainEntry{RecordType: 'u', XY: string(fields[1]), Path: string(fields[10])})
		default:
			return nil, malformedConflictOutput("unknown porcelain record")
		}
	}
	return entries, nil
}

func validPorcelainFields(fields [][]byte, oidIndices ...int) bool {
	if len(fields[1]) != 2 || len(fields[len(fields)-1]) == 0 {
		return false
	}
	for _, index := range oidIndices {
		if !validObjectID(string(fields[index])) {
			return false
		}
	}
	return true
}

func validRenameScore(value []byte) bool {
	return len(value) == 4 && (value[0] == 'R' || value[0] == 'C') && validSimilarityScore(value[1:])
}

func validSimilarityScore(value []byte) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	score, err := strconv.Atoi(string(value))
	return err == nil && score >= 0 && score <= 100
}

func parseIndexStagesZ(data []byte) ([]indexStage, error) {
	parts, err := splitNULTerminated(data)
	if err != nil {
		return nil, err
	}
	stages := make([]indexStage, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, record := range parts {
		metadata, path, found := bytes.Cut(record, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !found || len(path) == 0 || len(fields) != 3 || !validMode(fields[0]) || !validObjectID(string(fields[1])) || len(fields[2]) != 1 || fields[2][0] < '0' || fields[2][0] > '3' {
			return nil, malformedConflictOutput("invalid index stage")
		}
		stage := int(fields[2][0] - '0')
		key := string(path) + "\x00" + string(fields[2])
		if _, exists := seen[key]; exists {
			return nil, malformedConflictOutput("duplicate index stage")
		}
		seen[key] = struct{}{}
		stages = append(stages, indexStage{Path: string(path), Mode: string(fields[0]), OID: string(fields[1]), Stage: stage})
	}
	return stages, nil
}

func validMode(mode []byte) bool {
	if len(mode) != 6 {
		return false
	}
	for _, character := range mode {
		if character < '0' || character > '7' {
			return false
		}
	}
	return true
}

func parseNameStatusZ(data []byte) ([]nameStatus, error) {
	parts, err := splitNULTerminated(data)
	if err != nil {
		return nil, err
	}
	entries := make([]nameStatus, 0, len(parts))
	for index := 0; index < len(parts); index++ {
		status := parts[index]
		if len(status) == 1 && (status[0] == 'M' || status[0] == 'D') {
			if index+1 >= len(parts) || len(parts[index+1]) == 0 {
				return nil, malformedConflictOutput("truncated name-status record")
			}
			entries = append(entries, nameStatus{Status: status[0], Path: string(parts[index+1])})
			index++
			continue
		}
		if len(status) != 4 || (status[0] != 'R' && status[0] != 'C') || !validSimilarityScore(status[1:]) || index+2 >= len(parts) || len(parts[index+1]) == 0 || len(parts[index+2]) == 0 {
			return nil, malformedConflictOutput("invalid name-status record")
		}
		score, err := strconv.Atoi(string(status[1:]))
		if err != nil || score < 0 || score > 100 {
			return nil, malformedConflictOutput("invalid rename score")
		}
		entries = append(entries, nameStatus{Status: status[0], Score: score, OldPath: string(parts[index+1]), Path: string(parts[index+2])})
		index += 2
	}
	return entries, nil
}

func parseCheckAttrZ(data []byte) ([]attributeRecord, error) {
	parts, err := splitNULTerminated(data)
	if err != nil {
		return nil, err
	}
	if len(parts)%3 != 0 {
		return nil, malformedConflictOutput("truncated attribute record")
	}
	attributes := make([]attributeRecord, 0, len(parts)/3)
	for index := 0; index < len(parts); index += 3 {
		if len(parts[index]) == 0 {
			return nil, malformedConflictOutput("empty attribute path")
		}
		attributes = append(attributes, attributeRecord{Path: string(parts[index]), Name: string(parts[index+1]), Value: string(parts[index+2])})
	}
	return attributes, nil
}

func parseCheckoutTempZ(data []byte) ([]checkoutTemp, error) {
	parts, err := splitNULTerminated(data)
	if err != nil {
		return nil, err
	}
	temps := make([]checkoutTemp, 0, len(parts))
	for _, record := range parts {
		temp, path, found := bytes.Cut(record, []byte{'\t'})
		if !found || len(path) == 0 || !filepath.IsLocal(string(temp)) {
			return nil, malformedConflictOutput("invalid checkout temporary path")
		}
		temps = append(temps, checkoutTemp{TempPath: string(temp), Path: string(path)})
	}
	return temps, nil
}

func malformedConflictOutput(detail string) error {
	return fmt.Errorf("%w: %s", ErrOutputMalformed, detail)
}
