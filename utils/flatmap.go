package utils

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	flatmapPathSeparator = "."
)

// UnknownVariableValue marks an unknown Terraform flat-map value.
const UnknownVariableValue = "74D93920-ED26-11E3-AC10-0800200C9A66"

// Expand takes a map and a key (prefix) and expands that value into
// a more complex structure. This is the reverse of the Flatten operation.
func Expand(flat map[string]string, key string) any {
	// If the key is exactly a key in the map, just return it
	if value, ok := flat[key]; ok {
		switch value {
		case "true":
			return true
		case "false":
			return false
		}

		return value
	}

	// Check if the key is an array, and if so, expand the array
	if v, ok := flat[key+".#"]; ok {
		// If the count of the key is unknown, then just put the unknown
		// value in the value itself. This will be detected by Terraform
		// core later.
		if v == UnknownVariableValue {
			return v
		}

		return expandArray(flat, key)
	}

	// Check if this is a prefix in the map
	prefix := key + flatmapPathSeparator
	for mapKey := range flat {
		if strings.HasPrefix(mapKey, prefix) {
			return expandMap(flat, prefix)
		}
	}

	return nil
}

func expandArray(flat map[string]string, prefix string) []any {
	num, err := strconv.ParseInt(flat[prefix+".#"], 0, 0)
	if err != nil {
		panic(err)
	}

	// If the number of elements in this array is 0, then return an
	// empty slice as there is nothing to expand. Trying to expand it
	// anyway could lead to crashes as any child maps, arrays or sets
	// that no longer exist are still shown as empty with a count of 0.
	if num == 0 {
		return []any{}
	}

	// NOTE: "num" is not necessarily accurate, e.g. if a user tampers
	// with state, so the following code should not crash when given a
	// number of items more or less than what's given in num. The
	// num key is mainly just a hint that this is a list or set.

	// The Schema "Set" type stores its values in an array format, but
	// using numeric hash values instead of ordinal keys. Take the set
	// of keys regardless of value, and expand them in numeric order.
	// See GH-11042 for more details.
	keySet := map[int]bool{}
	computed := map[string]bool{}

	for mapKey := range flat {
		if !strings.HasPrefix(mapKey, prefix+flatmapPathSeparator) {
			continue
		}

		key := mapKey[len(prefix)+1:]

		key, _, _ = strings.Cut(key, flatmapPathSeparator)

		// skip the count value
		if key == "#" {
			continue
		}

		// strip the computed flag if there is one
		originalKey := key
		key = strings.TrimPrefix(key, "~")
		computed[key] = computed[key] || key != originalKey

		keySet[parseArrayIndex(key)] = true
	}

	keysList := make([]int, 0, num)
	for key := range keySet {
		keysList = append(keysList, key)
	}

	slices.Sort(keysList)

	result := make([]any, len(keysList))
	for position, key := range keysList {
		keyString := strconv.Itoa(key)
		if computed[keyString] {
			keyString = "~" + keyString
		}

		result[position] = Expand(flat, fmt.Sprintf("%s.%s", prefix, keyString))
	}

	return result
}

func parseArrayIndex(key string) int {
	index, err := strconv.Atoi(key)
	if err != nil {
		panic(err)
	}

	return index
}

func expandMap(flat map[string]string, prefix string) map[string]any {
	// Submaps may not have a '%' key, so we can't count on this value being
	// here. If we don't have a count, just proceed as if we have a map.
	if count, ok := flat[prefix+"%"]; ok && count == "0" {
		return map[string]any{}
	}

	result := make(map[string]any)

	for mapKey := range flat {
		if !strings.HasPrefix(mapKey, prefix) {
			continue
		}

		key := mapKey[len(prefix):]

		key, _, _ = strings.Cut(key, flatmapPathSeparator)

		if _, ok := result[key]; ok {
			continue
		}

		// skip the map count value
		if key == "%" {
			continue
		}

		result[key] = Expand(flat, mapKey[:len(prefix)+len(key)])
	}

	return result
}
