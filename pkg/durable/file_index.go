package durable

import (
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

const maxFileIndexCandidateMessages = 200

func contextAttachments(attachments []execution.Attachment) []contextbuilder.Attachment {
	var converted []contextbuilder.Attachment
	for _, a := range attachments {
		converted = append(converted, contextbuilder.Attachment{
			ID: a.ID, Kind: string(a.Kind), URI: a.URI, Name: a.Name, MediaType: a.MediaType,
			SizeBytes: a.SizeBytes, ArtifactURI: a.ArtifactURI,
		})
	}
	return converted
}

// loadSessionFileIndex reads the latest occurrence of each file from completed
// runs, independently of the conversation compaction cursor. Sandbox files
// are accessible only through a confirmed checkpoint in the restore chain.
func loadSessionFileIndex(
	tx *gorm.DB, owner, session string, filesystem *execution.FilesystemBinding,
) ([]contextbuilder.FileIndexEntry, error) {
	var provider, endpoint, workspace, restore string
	if filesystem != nil {
		provider, endpoint, workspace = filesystem.Provider, filesystem.URL, filesystem.Workspace
		if provider == "Sandbox" {
			restore = filesystem.RestoreFromSandboxID
		}
	}
	tenantFilter := ""
	if scope, scoped := access.FromContext(tx.Statement.Context); scoped {
		tenantFilter = scope.TenantID
	}
	const query = `
WITH RECURSIVE completed AS (
	SELECT r.id, r.epoch, r.spec->'filesystem' AS filesystem
	FROM durable_run AS r
	JOIN agent_session AS s ON s.id = r.session_id AND s.tenant_id = r.tenant_id AND s.owner_id = r.owner_id
	WHERE r.session_id = ? AND r.owner_id = ? AND r.status = 'Completed'
	  AND (? = '' OR r.tenant_id = ?)
), restored(sandbox_id) AS (
	SELECT CAST(? AS text) WHERE ? <> ''
	UNION
	SELECT r.filesystem->>'restoreFromSandboxId'
	FROM restored AS ancestry
	JOIN durable_run_environment AS e ON e.sandbox_id = ancestry.sandbox_id
	JOIN completed AS r ON r.id = e.run_id AND r.epoch = e.run_epoch
	WHERE e.status = 'Released' AND e.saved_checkpoint <> ''
	  AND r.filesystem->>'restoreFromSandboxId' <> ''
), candidates AS (
	SELECT r.id, r.epoch, r.filesystem, m.seq, m.kind, m.message
	FROM completed AS r
	JOIN agent_message AS m ON m.run_id = r.id AND m.session_id = ?
	WHERE (m.kind = 'User' AND jsonb_typeof(m.message->'attachments') = 'array' AND jsonb_array_length(m.message->'attachments') > 0)
	   OR (m.kind = 'Tool' AND jsonb_typeof(m.message->'artifacts') = 'array' AND jsonb_array_length(m.message->'artifacts') > 0)
	ORDER BY m.seq DESC
	LIMIT ?
), indexed AS (
	SELECT c.id, c.epoch, c.filesystem, c.seq, entry.position, entry.value,
		CASE c.kind WHEN 'User' THEN 'attachment' ELSE 'workspace' END AS source,
		CASE c.kind WHEN 'User' THEN entry.value->>'uri' ELSE entry.value->>'path' END AS uri
	FROM candidates AS c
	CROSS JOIN LATERAL jsonb_array_elements(
		CASE c.kind WHEN 'User' THEN c.message->'attachments' ELSE c.message->'artifacts' END
	) WITH ORDINALITY AS entry(value, position)
), unique_files AS (
	SELECT DISTINCT ON (source, uri) *
	FROM indexed WHERE uri <> ''
	ORDER BY source, uri, seq DESC, position DESC
), recent AS (
	SELECT * FROM unique_files ORDER BY seq DESC, position DESC LIMIT ?
)
SELECT uri, value->>'name' AS name, value->>'mediaType' AS media_type,
	(value->>'sizeBytes')::bigint AS size_bytes, source,
	CASE source WHEN 'attachment' THEN 'uploaded' ELSE value->>'action' END AS action,
	COALESCE(CASE
			WHEN source = 'attachment' THEN false
			WHEN COALESCE(NULLIF(r.value->>'provider', ''), r.filesystem->>'provider') = 'AIO'
				THEN ? = 'AIO' AND r.filesystem->>'provider' = 'AIO'
					AND r.filesystem->>'url' = ? AND r.filesystem->>'workspace' = ?
			WHEN COALESCE(NULLIF(r.value->>'provider', ''), r.filesystem->>'provider') = 'Sandbox'
				THEN EXISTS (
					SELECT 1 FROM durable_run_environment AS e
					JOIN restored AS ancestry ON ancestry.sandbox_id = e.sandbox_id
					WHERE e.run_id = r.id AND e.run_epoch = r.epoch
						AND e.status = 'Released' AND e.saved_checkpoint <> ''
				)
			ELSE false
		END, false) AS accessible
FROM recent AS r ORDER BY seq DESC, position DESC`
	var files []contextbuilder.FileIndexEntry
	err := tx.Raw(query, session, owner, tenantFilter, tenantFilter, restore, restore,
		session, maxFileIndexCandidateMessages, contextbuilder.MaxFileIndexEntries,
		provider, endpoint, workspace).Scan(&files).Error
	return files, err
}
