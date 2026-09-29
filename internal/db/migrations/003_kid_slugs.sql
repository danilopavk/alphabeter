-- A kid's URL segment ("ana-marija" in /kids/ana-marija/play), so URLs don't show
-- database IDs. Unique per parent; internal/kids makes it from the name. Kids
-- added before get theirs here: runs of anything but letters and digits become
-- one dash, and "kid" when nothing is left.
ALTER TABLE kids ADD COLUMN slug text;
UPDATE kids SET slug = coalesce(
    nullif(trim(BOTH '-' FROM lower(regexp_replace(name, '[^[:alnum:]]+', '-', 'g'))), ''), 'kid');
-- A parent's kids whose names give the same slug: all but the first get their ID appended.
UPDATE kids SET slug = kids.slug || '-' || kids.id
FROM (SELECT id, row_number() OVER (PARTITION BY user_id, slug ORDER BY id) AS n FROM kids) AS dup
WHERE dup.id = kids.id AND dup.n > 1;
ALTER TABLE kids ALTER COLUMN slug SET NOT NULL;

-- Also serves listing a parent's kids, which kids_user_idx did.
DROP INDEX kids_user_idx;
CREATE UNIQUE INDEX kids_user_slug_idx ON kids (user_id, slug);
