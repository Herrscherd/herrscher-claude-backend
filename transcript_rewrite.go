package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func encodeJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func prunedInput(input string) string {
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return input
	}
	value, shortened := shortenStrings(value)
	if !shortened {
		return input
	}
	encoded, err := encodeJSON(value)
	if err != nil {
		return input
	}
	return string(encoded)
}

func shortenStrings(value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		if head := runeHead(typed, prunedInputRunes); len(head) < len(typed) {
			return head + "…", true
		}
		return typed, false
	case map[string]any:
		shortenedAny := false
		for key, inner := range typed {
			var shortened bool
			typed[key], shortened = shortenStrings(inner)
			shortenedAny = shortenedAny || shortened
		}
		return typed, shortenedAny
	case []any:
		shortenedAny := false
		for index, inner := range typed {
			var shortened bool
			typed[index], shortened = shortenStrings(inner)
			shortenedAny = shortenedAny || shortened
		}
		return typed, shortenedAny
	}
	return value, false
}

type pruneEdit struct {
	contracts.HistoryEdit

	input   string
	content json.RawMessage
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func contentBlocks(content json.RawMessage) ([]json.RawMessage, bool) {
	var blocks []json.RawMessage
	return blocks, json.Unmarshal(content, &blocks) == nil
}

func prunedContent(edit contracts.HistoryEdit, content json.RawMessage) (json.RawMessage, error) {
	before := resultText(content)
	if edit.Action != contracts.PruneCall && alreadyPruned(before, edit.KeepChars, edit.KeepTail) {
		return nil, nil
	}
	rewritten, err := rewrittenContent(edit, content)
	if err != nil || rewritten == nil {
		return nil, err
	}
	if utf8.RuneCountInString(resultText(rewritten)) >= utf8.RuneCountInString(before) {
		return nil, nil
	}
	return rewritten, nil
}

func rewrittenContent(edit contracts.HistoryEdit, content json.RawMessage) (json.RawMessage, error) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return prunedTextContent(edit, text)
	}
	blocks, isList := contentBlocks(content)
	if !isList {
		return nil, nil
	}
	var pruned []json.RawMessage
	var err error
	if edit.Action == contracts.PruneCall {
		pruned, err = noteBlocks(blocks)
	} else {
		pruned, err = truncatedBlocks(blocks, edit.KeepChars, edit.KeepTail)
	}
	if err != nil {
		return nil, err
	}
	return jsonArray(pruned), nil
}

func jsonArray(items []json.RawMessage) json.RawMessage {
	array := []byte{'['}
	for index, item := range items {
		if index > 0 {
			array = append(array, ',')
		}
		array = append(array, item...)
	}
	return append(array, ']')
}

func prunedTextContent(edit contracts.HistoryEdit, text string) (json.RawMessage, error) {
	result, changed := prunedCallNote, text != prunedCallNote
	if edit.Action != contracts.PruneCall {
		result, changed = prunedResult(text, edit.KeepChars, edit.KeepTail)
	}
	if !changed {
		return nil, nil
	}
	encoded, err := encodeJSON(result)
	if err != nil {
		return nil, fmt.Errorf("encode pruned claude tool result: %w", err)
	}
	return encoded, nil
}

func blockOfText(raw json.RawMessage) (string, bool) {
	var block textBlock
	if json.Unmarshal(raw, &block) != nil || block.Type != blockText {
		return "", false
	}
	return block.Text, true
}

func noteBlocks(blocks []json.RawMessage) ([]json.RawMessage, error) {
	note, err := encodeJSON(textBlock{Type: blockText, Text: prunedCallNote})
	if err != nil {
		return nil, fmt.Errorf("encode pruned claude tool result: %w", err)
	}
	kept := make([]json.RawMessage, 0, len(blocks)+1)
	placed := false
	for _, block := range blocks {
		if _, isText := blockOfText(block); !isText {
			kept = append(kept, block)
			continue
		}
		if !placed {
			kept = append(kept, note)
			placed = true
		}
	}
	if !placed {
		kept = append(kept, note)
	}
	return kept, nil
}

func blockTexts(blocks []json.RawMessage) []string {
	texts := make([]string, len(blocks))
	for index, block := range blocks {
		texts[index], _ = blockOfText(block)
	}
	return texts
}

func truncatedBlocks(blocks []json.RawMessage, keep, keepTail int) ([]json.RawMessage, error) {
	cut := planPrunedCut(blockTexts(blocks), textRunes(blocks), keep, keepTail)
	if cut.removed <= 0 {
		return blocks, nil
	}
	note := fmt.Sprintf(prunedResultNote, cut.missing)
	kept := make([]json.RawMessage, 0, len(blocks))
	offset := 0
	for _, block := range blocks {
		text, isText := blockOfText(block)
		if !isText {
			kept = append(kept, block)
			continue
		}
		start := offset
		offset += utf8.RuneCountInString(text)
		if offset <= cut.head || start >= cut.tailStart {
			kept = append(kept, block)
			continue
		}
		cutText := joinText(joinText(runeHead(text, max(0, cut.head-start)), note), runeTail(text, offset-max(start, cut.tailStart)))
		note = ""
		if cutText == "" {
			continue
		}
		rewritten, err := withText(block, cutText)
		if err != nil {
			return nil, err
		}
		kept = append(kept, rewritten)
	}
	return kept, nil
}

func withText(block json.RawMessage, text string) (json.RawMessage, error) {
	encodedText, err := encodeJSON(text)
	if err != nil {
		return nil, fmt.Errorf("encode claude text block: %w", err)
	}
	rewritten, err := replaceField(block, "text", encodedText)
	if err != nil {
		return nil, err
	}
	return rewritten.bytes, nil
}

type byteSpan struct {
	start int
	end   int
	found bool
}

type rewrite struct {
	bytes   []byte
	changed bool
}

func fieldSpan(object []byte, key string) (byteSpan, error) {
	decoder := json.NewDecoder(bytes.NewReader(object))
	opening, err := decoder.Token()
	if err != nil {
		return byteSpan{}, fmt.Errorf("decode claude transcript object: %w", err)
	}
	if opening != json.Delim('{') {
		return byteSpan{}, nil
	}
	for decoder.More() {
		token, tokenErr := decoder.Token()
		if tokenErr != nil {
			return byteSpan{}, fmt.Errorf("decode claude transcript object: %w", tokenErr)
		}
		var value json.RawMessage
		if decodeErr := decoder.Decode(&value); decodeErr != nil {
			return byteSpan{}, fmt.Errorf("decode claude transcript object: %w", decodeErr)
		}
		if name, isKey := token.(string); isKey && name == key {
			end := int(decoder.InputOffset())
			return byteSpan{start: end - len(value), end: end, found: true}, nil
		}
	}
	return byteSpan{}, nil
}

func splice(object []byte, span byteSpan, value []byte) []byte {
	spliced := make([]byte, 0, len(object)-(span.end-span.start)+len(value))
	spliced = append(spliced, object[:span.start]...)
	spliced = append(spliced, value...)
	return append(spliced, object[span.end:]...)
}

func replaceField(object []byte, key string, value []byte) (rewrite, error) {
	span, err := fieldSpan(object, key)
	if err != nil || !span.found || bytes.Equal(object[span.start:span.end], value) {
		return rewrite{bytes: object}, err
	}
	return rewrite{bytes: splice(object, span, value), changed: true}, nil
}

func prunedLine(line []byte, plan map[string]pruneEdit) ([]byte, error) {
	entry := bytes.TrimRight(line, "\r\n")
	messageSpan, err := fieldSpan(entry, "message")
	if err != nil || !messageSpan.found {
		return line, err
	}
	message := entry[messageSpan.start:messageSpan.end]
	contentSpan, err := fieldSpan(message, "content")
	if err != nil || !contentSpan.found {
		return line, err
	}
	content, err := prunedBlocks(message[contentSpan.start:contentSpan.end], plan)
	if err != nil || !content.changed {
		return line, err
	}
	pruned := splice(entry, messageSpan, splice(message, contentSpan, content.bytes))
	return append(pruned, line[len(entry):]...), nil
}

func prunedBlocks(content json.RawMessage, plan map[string]pruneEdit) (rewrite, error) {
	if !bytes.HasPrefix(content, []byte("[")) {
		return rewrite{bytes: content}, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return rewrite{}, fmt.Errorf("decode claude message content: %w", err)
	}
	changed := false
	for index, block := range blocks {
		rewritten, err := prunedBlock(block, plan)
		if err != nil {
			return rewrite{}, err
		}
		blocks[index] = rewritten.bytes
		changed = changed || rewritten.changed
	}
	if !changed {
		return rewrite{bytes: content}, nil
	}
	return rewrite{bytes: jsonArray(blocks), changed: true}, nil
}

type blockHeader struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ToolUseID string `json:"tool_use_id"`
}

func prunedBlock(block json.RawMessage, plan map[string]pruneEdit) (rewrite, error) {
	var header blockHeader
	if err := json.Unmarshal(block, &header); err != nil {
		return rewrite{}, fmt.Errorf("decode claude message block: %w", err)
	}
	switch header.Type {
	case blockToolUse:
		if input := plan[header.ID].input; input != "" {
			return replaceField(block, "input", []byte(input))
		}
	case blockToolResult:
		if content := plan[header.ToolUseID].content; content != nil {
			return replaceField(block, "content", content)
		}
	}
	return rewrite{bytes: block}, nil
}
