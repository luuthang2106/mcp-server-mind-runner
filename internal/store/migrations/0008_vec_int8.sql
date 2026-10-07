-- vector lưu int8 (dim byte) + scale thay cho float32 (dim*4 byte): RAM/đĩa
-- giảm 4 lần, recall quét thẳng từ SQLite (không cache trong RAM mỗi process).
-- scale NULL = row float32 cũ; Store.Migrate chuyển dần, quét hiểu cả hai.
ALTER TABLE embeddings ADD COLUMN scale REAL;
