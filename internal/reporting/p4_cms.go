package reporting

// Reports of Landing Page & CMS (PRD P4 EP-24, FR-RPT-P4-05 permission per
// report) on the cms_* reporting views: Website Content Report, Website
// Publishing Report (the publishing log, FR-CMS-10) and Website
// Translation Report (FR-CMS-05). Report names follow Naming Convention §32.

var cmsKinds = []string{"page", "article", "banner", "gallery"}

var cmsReports = []*Report{
	sqlReport("cms.website_content", "Website Content Report", "cms",
		"Pages, news, banners and albums changed in the period or currently live / scheduled: status, live and latest version, unpublished changes, go-live and take-down.",
		cols("kind|Type", "title|Title", "key|Key", "status|Status", "live|Live", "publishedVersion|Live Version|number", "latestVersion|Latest Version|number",
			"unpublishedChanges|Unpublished Changes", "publishAt|Go-live|datetime", "unpublishAt|Take-down|datetime", "publishedAt|Published|datetime",
			"updatedAt|Updated|datetime", "updatedBy|Updated By"),
		[]Param{{Key: "kind", Label: "Content Type", Type: "enum", Enum: cmsKinds},
			{Key: "status", Label: "Status", Type: "enum", Enum: []string{"draft", "in_review", "scheduled", "published", "unpublished"}}}, 90,
		`SELECT kind, title, key, status, live, published_version AS "publishedVersion", latest_version AS "latestVersion",
		has_unpublished_changes AS "unpublishedChanges", publish_at AS "publishAt", unpublish_at AS "unpublishAt", published_at AS "publishedAt",
		updated_at AS "updatedAt", updated_by_name AS "updatedBy"
		FROM reporting.cms_contents
		WHERE ((updated_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date OR live OR status = 'scheduled')
		  AND ($4 = '' OR kind = $4) AND ($5 = '' OR status = $5)
		ORDER BY kind, title`),
	sqlReport("cms.website_publishing", "Website Publishing Report", "cms",
		"Publishing log of the period: saved, submitted, approved, rejected, scheduled, published, unpublished, rolled back and archived content with the actor.",
		cols("at|At|datetime", "kind|Type", "title|Title", "action|Action", "version|Version|number", "actor|By", "note|Note"),
		[]Param{{Key: "kind", Label: "Content Type", Type: "enum", Enum: cmsKinds},
			{Key: "action", Label: "Action", Type: "enum", Enum: []string{"created", "saved", "submitted", "approved", "rejected", "withdrawn", "scheduled",
				"published", "unpublished", "restored", "rolled_back", "archived", "imported"}}}, 30,
		`SELECT at, kind, title, action, version_no AS "version", actor_name AS "actor", note
		FROM reporting.cms_publish_events WHERE (at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		  AND ($4 = '' OR kind = $4) AND ($5 = '' OR action = $5)
		ORDER BY at DESC, title`),
	sqlReport("cms.website_translation", "Website Translation Report", "cms",
		"Translation status per content item and website language (missing, incomplete, outdated, complete) with the website path.",
		cols("kind|Type", "title|Title", "status|Status", "live|Live", "language|Language", "translation|Translation", "translatedTitle|Translated Title",
			"path|Path", "updatedAt|Updated|datetime"),
		[]Param{{Key: "language", Label: "Language", Type: "string"},
			{Key: "translation", Label: "Translation", Type: "enum", Enum: []string{"missing", "incomplete", "outdated", "complete"}}}, 365,
		`SELECT kind, title, status, live, language, translation_status AS "translation", translated_title AS "translatedTitle", path, updated_at AS "updatedAt"
		FROM reporting.cms_translations WHERE $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''
		  AND ($4 = '' OR language = $4) AND ($5 = '' OR translation_status = $5)
		ORDER BY kind, title, language`),
}
