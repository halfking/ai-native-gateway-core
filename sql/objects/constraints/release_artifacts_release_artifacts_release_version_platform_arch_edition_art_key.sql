--
-- Name: release_artifacts release_artifacts_release_version_platform_arch_edition_art_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_artifacts
    ADD CONSTRAINT release_artifacts_release_version_platform_arch_edition_art_key UNIQUE (release_version, platform, arch, edition, artifact_name);

