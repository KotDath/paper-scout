-- Membership is created by the trusted importer, never by search arguments.
CREATE TABLE user_papers (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    paper_version_id TEXT NOT NULL REFERENCES paper_versions(id),
    saved INTEGER NOT NULL DEFAULT 0 CHECK (saved IN (0,1)),
    read INTEGER NOT NULL DEFAULT 0 CHECK (read IN (0,1)),
    note TEXT NOT NULL DEFAULT '',
    added_at TEXT NOT NULL,
    PRIMARY KEY (user_id, paper_version_id)
);
CREATE INDEX user_papers_version ON user_papers(paper_version_id);

CREATE TABLE document_indexes (
    document_id TEXT PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    model_id TEXT NOT NULL,
    model_digest TEXT NOT NULL,
    encoding_version TEXT NOT NULL,
    dimensions INTEGER NOT NULL,
    chunking_version TEXT NOT NULL,
    indexed_chunks INTEGER NOT NULL DEFAULT 0,
    total_chunks INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','ready','partial','failed')),
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE chunks_fts USING fts5(chunk_id UNINDEXED, title, section, text, tokenize='unicode61');
INSERT INTO chunks_fts(chunk_id,title,section,text)
SELECT c.id,p.title,c.section,c.text FROM chunks c JOIN documents d ON d.id=c.document_id JOIN paper_versions p ON p.id=d.paper_version_id;
CREATE TRIGGER t08_chunks_insert AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(chunk_id,title,section,text)
    SELECT new.id,p.title,new.section,new.text FROM documents d JOIN paper_versions p ON p.id=d.paper_version_id WHERE d.id=new.document_id;
    UPDATE document_indexes SET status='pending',indexed_chunks=0,total_chunks=(SELECT count(*) FROM chunks WHERE document_id=new.document_id),last_error='source_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE document_id=new.document_id;
END;
CREATE TRIGGER t08_chunks_delete AFTER DELETE ON chunks BEGIN
    DELETE FROM chunks_fts WHERE chunk_id=old.id;
    UPDATE document_indexes SET status='pending',indexed_chunks=0,total_chunks=(SELECT count(*) FROM chunks WHERE document_id=old.document_id),last_error='source_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE document_id=old.document_id;
END;
CREATE TRIGGER t08_chunks_update AFTER UPDATE ON chunks BEGIN
    DELETE FROM chunks_fts WHERE chunk_id=old.id;
    INSERT INTO chunks_fts(chunk_id,title,section,text)
    SELECT new.id,p.title,new.section,new.text FROM documents d JOIN paper_versions p ON p.id=d.paper_version_id WHERE d.id=new.document_id;
    UPDATE document_indexes SET status='pending',indexed_chunks=0,total_chunks=(SELECT count(*) FROM chunks WHERE document_id=new.document_id),last_error='source_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE document_id=new.document_id;
END;
CREATE TRIGGER t08_paper_title AFTER UPDATE OF title ON paper_versions WHEN new.title != old.title BEGIN
    UPDATE chunks_fts SET title=new.title WHERE chunk_id IN
      (SELECT c.id FROM chunks c JOIN documents d ON d.id=c.document_id WHERE d.paper_version_id=new.id);
    UPDATE document_indexes SET status='pending',indexed_chunks=0,last_error='source_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE document_id IN (SELECT id FROM documents WHERE paper_version_id=new.id);
END;

-- Mode is always document; query vectors are never persisted in this cache.
CREATE TABLE chunk_embeddings (
    chunk_id TEXT NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    model_id TEXT NOT NULL,
    model_digest TEXT NOT NULL,
    encoding_version TEXT NOT NULL,
    dimensions INTEGER NOT NULL CHECK (dimensions=1024),
    chunking_version TEXT NOT NULL,
    input_hash TEXT NOT NULL CHECK (length(input_hash)=64),
    vector_blob BLOB NOT NULL CHECK (length(vector_blob)=4096),
    PRIMARY KEY (chunk_id,model_id,model_digest,encoding_version,dimensions,chunking_version,input_hash)
);
CREATE INDEX t08_embedding_cache ON chunk_embeddings(model_id,model_digest,encoding_version,dimensions,chunking_version,input_hash);
