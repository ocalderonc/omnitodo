-- +goose Up
-- An activity can belong to a company directly, without a project. With a
-- project, the company comes from the project and company_id stays NULL, so
-- the two can never disagree.
ALTER TABLE activity ADD COLUMN company_id INTEGER REFERENCES company (id) ON DELETE SET NULL
    CHECK (company_id IS NULL OR project_id IS NULL);

-- +goose Down
ALTER TABLE activity DROP COLUMN company_id;
