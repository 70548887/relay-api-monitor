package upstream

import "io"

type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += int64(n)
	return n, err
}
func readGenerationJSON(reader io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(reader, maxGenerationJSONBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxGenerationJSONBytes {
		return nil, &Error{Code: "response_json_too_large", Message: "上游生成 JSON 超过 8 MiB，已停止接收"}
	}
	return b, nil
}
