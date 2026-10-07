-- Course Guide (PRD P2 FR-PLX-01): a picture of a P1 course asset (course
-- map, hole layout, panorama) uploaded from the Back Office like a product
-- photo, next to P1's file reference (expand only).

-- +goose Up
ALTER TABLE golf.course_assets ADD COLUMN image_url text;

-- +goose Down
ALTER TABLE golf.course_assets DROP COLUMN image_url;
