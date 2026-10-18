-- Durable projection queue: origin commits survive a crash before capture.
CREATE TABLE research_memory_jobs (
 owner TEXT NOT NULL REFERENCES users(id), kind TEXT NOT NULL CHECK(kind IN ('report','note')),
 source_id TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1, processed INTEGER NOT NULL DEFAULT 0,
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 status TEXT NOT NULL DEFAULT 'pending', detail TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(owner,kind,source_id)
);
INSERT INTO research_memory_jobs(owner,kind,source_id) SELECT user_id,'report',id FROM reports;
INSERT INTO research_memory_jobs(owner,kind,source_id) SELECT user_id,'note',paper_version_id FROM user_papers WHERE trim(note)!='';
-- Preserve every explicit edit, even when several edits precede projection.
CREATE TABLE research_memory_note_history (
 owner TEXT NOT NULL REFERENCES users(id), paper_id TEXT NOT NULL REFERENCES paper_versions(id),
 generation INTEGER NOT NULL, text TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(owner,paper_id,generation)
);
INSERT INTO research_memory_note_history
 SELECT j.owner,j.source_id,j.generation,u.note,j.updated_at FROM research_memory_jobs j
 JOIN user_papers u ON u.user_id=j.owner AND u.paper_version_id=j.source_id WHERE j.kind='note';
CREATE TRIGGER t19_report_capture AFTER INSERT ON reports BEGIN
 INSERT INTO research_memory_jobs(owner,kind,source_id) VALUES(new.user_id,'report',new.id);
END;
CREATE TABLE research_memories (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL REFERENCES users(id), kind TEXT NOT NULL,
 facet TEXT NOT NULL DEFAULT '', paper_id TEXT NOT NULL DEFAULT '', target_id TEXT NOT NULL DEFAULT '',
 title TEXT NOT NULL, text TEXT NOT NULL, content_hash TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1, excluded INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL,
 UNIQUE(owner,kind,paper_id,facet,content_hash)
);
CREATE INDEX t19_memory_owner ON research_memories(owner,kind,paper_id);
CREATE TABLE research_memory_origins (
 memory_id TEXT NOT NULL REFERENCES research_memories(id) ON DELETE CASCADE,
 report_id TEXT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
 analysis_id TEXT NOT NULL DEFAULT '', document_id TEXT NOT NULL DEFAULT '',
 metadata_json TEXT NOT NULL CHECK(json_valid(metadata_json)),
 PRIMARY KEY(memory_id,report_id,analysis_id)
);
CREATE TABLE research_memory_revisions (
 memory_id TEXT NOT NULL REFERENCES research_memories(id), revision INTEGER NOT NULL,
 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)), created_at TEXT NOT NULL,
 PRIMARY KEY(memory_id,revision)
);
CREATE TABLE research_memory_passages (
 id TEXT PRIMARY KEY, memory_id TEXT NOT NULL REFERENCES research_memories(id) ON DELETE CASCADE,
 ordinal INTEGER NOT NULL, char_start INTEGER NOT NULL, char_end INTEGER NOT NULL,
 text TEXT NOT NULL, input_hash TEXT NOT NULL, revision INTEGER NOT NULL,
 UNIQUE(memory_id,ordinal)
);
CREATE VIRTUAL TABLE research_memory_fts USING fts5(passage_id UNINDEXED,title,text,tokenize='unicode61');
CREATE TRIGGER t19_passage_insert AFTER INSERT ON research_memory_passages BEGIN
 INSERT INTO research_memory_fts(passage_id,title,text) SELECT new.id,title,new.text FROM research_memories WHERE id=new.memory_id;
END;
CREATE TRIGGER t19_passage_delete AFTER DELETE ON research_memory_passages BEGIN
 DELETE FROM research_memory_fts WHERE passage_id=old.id;
END;
CREATE TABLE research_memory_vectors (
 passage_id TEXT PRIMARY KEY REFERENCES research_memory_passages(id) ON DELETE CASCADE,
 model_id TEXT NOT NULL, model_digest TEXT NOT NULL, encoding_version TEXT NOT NULL, dimensions INTEGER NOT NULL,
 input_hash TEXT NOT NULL, vector_blob BLOB NOT NULL
);
-- Invalidate synchronously, including while an Ollama request is in flight.
CREATE TRIGGER t19_note_update AFTER UPDATE OF note ON user_papers WHEN old.note!=new.note BEGIN
 INSERT INTO research_memory_jobs(owner,kind,source_id) VALUES(new.user_id,'note',new.paper_version_id)
 ON CONFLICT(owner,kind,source_id) DO UPDATE SET generation=generation+1,status='pending',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now');
 INSERT INTO research_memory_note_history SELECT owner,source_id,generation,new.note,updated_at FROM research_memory_jobs WHERE owner=new.user_id AND kind='note' AND source_id=new.paper_version_id;
 DELETE FROM research_memory_passages WHERE memory_id IN
 (SELECT id FROM research_memories WHERE owner=new.user_id AND kind='user_note' AND paper_id=new.paper_version_id);
END;
CREATE TRIGGER t19_note_insert AFTER INSERT ON user_papers WHEN trim(new.note)!='' BEGIN
 INSERT INTO research_memory_jobs(owner,kind,source_id) VALUES(new.user_id,'note',new.paper_version_id)
 ON CONFLICT(owner,kind,source_id) DO UPDATE SET generation=generation+1,status='pending',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now');
 INSERT INTO research_memory_note_history SELECT owner,source_id,generation,new.note,updated_at FROM research_memory_jobs WHERE owner=new.user_id AND kind='note' AND source_id=new.paper_version_id;
END;
