package admin

import "context"

// forceEnableBindings runs inside the credential recovery transaction.
func forceEnableBindings(ctx context.Context, db dbExec, credentialID int, rawModel string) (int64, error) {
	if rawModel != "" {
		tag, err := db.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available = TRUE,
				unavailable_reason = NULL,
				unavailable_at = NULL,
				unavailable_recover_at = NULL,
				updated_at = NOW()
			FROM provider_models pm
			WHERE cmb.credential_id = $1
			  AND pm.id = cmb.provider_model_id
			  AND pm.raw_model_name = $2
		`, credentialID, rawModel)
		if err != nil {
			return 0, err
		}
		return tag.RowsAffected(), nil
	}

	// An empty model is the credential-wide operator action. Reset bindings
	// in the same transaction as credentials; clearing only the credential
	// row leaves these nodes excluded by v_routable_credential_models.
	tag, err := db.Exec(ctx, `
		UPDATE credential_model_bindings
		SET available = TRUE,
			unavailable_reason = NULL,
			unavailable_at = NULL,
			unavailable_recover_at = NULL,
			updated_at = NOW()
		WHERE credential_id = $1
	`, credentialID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
