#!/usr/bin/env python3
"""Export existing chunks from a read-only SQLite index; no embeddings or API calls."""
import argparse
import hashlib
import json
import pathlib
import sqlite3


def digest(data):
    return hashlib.sha256(data).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--index', default='../codex-chat-cli/artifacts/docindex/index.sqlite')
    parser.add_argument('--output', default='documents/chunks.json')
    args = parser.parse_args()
    source = pathlib.Path(args.index).resolve(strict=True)
    before = digest(source.read_bytes())
    with sqlite3.connect(source.as_uri() + '?mode=ro', uri=True) as db:
        db.execute('PRAGMA query_only=ON')
        info = json.loads(db.execute("SELECT value FROM document_index_meta WHERE key='info'").fetchone()[0])
        chunks = []
        for ident, strategy, source_name, start, payload in db.execute('SELECT id,strategy,source,start,payload FROM document_chunks ORDER BY strategy,source,start,id'):
            chunk = json.loads(payload)
            if (chunk['chunk_id'], chunk['strategy'], chunk['source'], chunk['start']) != (ident, strategy, source_name, start):
                raise ValueError('Index columns and chunk metadata differ')
            if digest(chunk['text'].encode()) != chunk['content_hash']:
                raise ValueError('Chunk content hash mismatch')
            chunks.append(chunk)
    if digest(source.read_bytes()) != before:
        raise ValueError('Source index changed during import')
    exported = {'version': 1, 'origin': {'repository': 'https://github.com/lagrange777/aichallenge_hw', 'index': 'artifacts/docindex/index.sqlite', 'index_sha256': before, 'corpus_hash': info['corpus_hash'], 'created_at': info['created_at'], 'embedding_model_in_source': info['embedding_model'], 'estimated_pages_at_400_words': info['estimated_pages_at_400_words']}, 'chunks': chunks}
    output = pathlib.Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = output.with_suffix('.tmp')
    temporary.write_text(json.dumps(exported, ensure_ascii=False, indent=2) + '\n')
    temporary.replace(output)
    print(f'Imported {len(chunks)} chunks without changing source index: {output}')


if __name__ == '__main__':
    main()
