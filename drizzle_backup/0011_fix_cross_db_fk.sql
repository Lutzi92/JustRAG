ALTER TABLE "document_chunks"
DROP CONSTRAINT "document_chunks_file_id_files_id_fk";
--> statement-breakpoint
ALTER TABLE "document_chunks" ADD COLUMN "kb_id" uuid NOT NULL;
--> statement-breakpoint
CREATE INDEX "document_chunks_kb_id_idx" ON "document_chunks" USING btree ("kb_id");