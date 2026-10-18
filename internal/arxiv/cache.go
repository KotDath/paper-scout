package arxiv

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (c *Client) cached(ctx context.Context, key string) (Result, bool, error) {
	var data string
	err := c.db.QueryRowContext(ctx, "SELECT result_json FROM arxiv_query_cache WHERE cache_key = ? AND expires_at > ?", key, time.Now().Unix()).Scan(&data)
	if err == sql.ErrNoRows {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	var result Result
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		return Result{}, false, errors.New("invalid arXiv cache entry")
	}
	result.CacheHit, result.Attempts = true, 0
	return result, true, nil
}

func (c *Client) save(ctx context.Context, key string, result Result) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, paper := range result.Papers {
		authors, err := json.Marshal(paper.Authors)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO paper_versions
			(id, arxiv_id, version, title, authors_json, abstract, abstract_url, html_url, pdf_url, published_at, updated_at, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(arxiv_id, version) DO UPDATE SET title=excluded.title, authors_json=excluded.authors_json,
			abstract=excluded.abstract, published_at=excluded.published_at, updated_at=excluded.updated_at, fetched_at=excluded.fetched_at`,
			paper.ID, paper.ArxivID, paper.Version, paper.Title, string(authors), paper.Abstract,
			paper.AbstractURL, paper.HTMLURL, paper.PDFURL,
			paper.PublishedAt.Format(time.RFC3339Nano), paper.UpdatedAt.Format(time.RFC3339Nano), result.FetchedAt.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO arxiv_query_cache(cache_key, result_json, fetched_at, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET result_json=excluded.result_json, fetched_at=excluded.fetched_at, expires_at=excluded.expires_at`,
		key, string(data), result.FetchedAt.Format(time.RFC3339Nano), result.FetchedAt.Add(24*time.Hour).Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}
