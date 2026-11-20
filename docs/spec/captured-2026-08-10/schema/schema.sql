-- Derived from: pg_dump --schema-only against the live database
-- Captured: 2026-08-10, from the running Hearth (Rust) stack on WSL k3s, namespace hearth.
-- This is OBSERVED BEHAVIOUR of a running system, not source. See PROVENANCE.md.
--
--
-- PostgreSQL database dump
--

\restrict ptgM2OhvmH1jtNx6q2c4i2KHKKl6vhw2G3NEleLNxpX34Diq8NE2ZOBn0YVFlju

-- Dumped from database version 16.14 (Debian 16.14-1.pgdg12+1)
-- Dumped by pg_dump version 16.14 (Debian 16.14-1.pgdg12+1)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: btree_gin; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS btree_gin WITH SCHEMA public;


--
-- Name: EXTENSION btree_gin; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION btree_gin IS 'support for indexing common datatypes in GIN';


--
-- Name: pg_stat_statements; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_stat_statements WITH SCHEMA public;


--
-- Name: EXTENSION pg_stat_statements; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pg_stat_statements IS 'track planning and execution statistics of all SQL statements executed';


--
-- Name: pg_trgm; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;


--
-- Name: EXTENSION pg_trgm; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pg_trgm IS 'text similarity measurement and index searching based on trigrams';


--
-- Name: pgcrypto; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;


--
-- Name: EXTENSION pgcrypto; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pgcrypto IS 'cryptographic functions';


--
-- Name: vector; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;


--
-- Name: EXTENSION vector; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION vector IS 'vector data type and ivfflat and hnsw access methods';


--
-- Name: AccessLevel; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."AccessLevel" AS ENUM (
    'view',
    'comment',
    'edit',
    'owner'
);


--
-- Name: OrganizationItJobType; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."OrganizationItJobType" AS ENUM (
    'UPDATE',
    'REMOVE'
);


--
-- Name: OrganizationStatus; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."OrganizationStatus" AS ENUM (
    'PILOT',
    'ENTERPRISE'
);


--
-- Name: SetAsDefault; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."SetAsDefault" AS ENUM (
    'ASK',
    'FORCE',
    'HIDE'
);


--
-- Name: anchor_table_name; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.anchor_table_name AS ENUM (
    'PdfPlaceableCommentAnchor',
    'PdfHighlightAnchor'
);


--
-- Name: comms_channel_type; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.comms_channel_type AS ENUM (
    'public',
    'private',
    'direct_message',
    'team'
);


--
-- Name: comms_participant_role; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.comms_participant_role AS ENUM (
    'owner',
    'admin',
    'member'
);


--
-- Name: crm_cleanup_job_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.crm_cleanup_job_status AS ENUM (
    'Init',
    'InProgress',
    'Complete',
    'Failed'
);


--
-- Name: document_sub_type_value; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.document_sub_type_value AS ENUM (
    'task',
    'snippet'
);


--
-- Name: email_backfill_job_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.email_backfill_job_status AS ENUM (
    'Init',
    'InProgress',
    'Complete',
    'Cancelled',
    'Failed'
);


--
-- Name: email_backfill_message_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.email_backfill_message_status AS ENUM (
    'InProgress',
    'Completed',
    'Failed',
    'Cancelled'
);


--
-- Name: email_backfill_thread_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.email_backfill_thread_status AS ENUM (
    'InProgress',
    'Skipped',
    'Completed',
    'Failed',
    'Cancelled'
);


--
-- Name: email_label_list_visibility_enum; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.email_label_list_visibility_enum AS ENUM (
    'LabelShow',
    'LabelShowIfUnread',
    'LabelHide'
);


--
-- Name: email_label_type_enum; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.email_label_type_enum AS ENUM (
    'System',
    'User'
);


--
-- Name: email_message_list_visibility_enum; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.email_message_list_visibility_enum AS ENUM (
    'Show',
    'Hide'
);


--
-- Name: email_recipient_type; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.email_recipient_type AS ENUM (
    'TO',
    'CC',
    'BCC'
);


--
-- Name: email_user_provider_enum; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.email_user_provider_enum AS ENUM (
    'GMAIL'
);


--
-- Name: entity_access_source_type; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.entity_access_source_type AS ENUM (
    'channel',
    'team',
    'user'
);


--
-- Name: github_app_installation_source_type; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.github_app_installation_source_type AS ENUM (
    'team',
    'user'
);


--
-- Name: import_run_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.import_run_status AS ENUM (
    'running',
    'ready',
    'importing',
    'completed',
    'failed',
    'dismissed'
);


--
-- Name: insights_backfill_batch_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.insights_backfill_batch_status AS ENUM (
    'Queued',
    'InProgress',
    'Complete',
    'Failed'
);


--
-- Name: insights_backfill_job_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.insights_backfill_job_status AS ENUM (
    'Init',
    'InProgress',
    'Complete',
    'Cancelled',
    'Failed'
);


--
-- Name: notification_device_type_option; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.notification_device_type_option AS ENUM (
    'ios',
    'android',
    'iosvoip'
);


--
-- Name: property_data_type; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.property_data_type AS ENUM (
    'BOOLEAN',
    'DATE',
    'NUMBER',
    'STRING',
    'SELECT_NUMBER',
    'SELECT_STRING',
    'ENTITY',
    'LINK',
    'TAG'
);


--
-- Name: property_entity_type; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.property_entity_type AS ENUM (
    'CHANNEL',
    'CHAT',
    'DOCUMENT',
    'PROJECT',
    'THREAD',
    'USER',
    'COMPANY',
    'TASK',
    'CALL_RECORD'
);


--
-- Name: team_plan; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.team_plan AS ENUM (
    'idea',
    'pre_seed',
    'seed',
    'series_a',
    'growth'
);


--
-- Name: team_role; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.team_role AS ENUM (
    'member',
    'admin',
    'owner'
);


--
-- Name: cascade_comms_message_delete_to_notifications(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.cascade_comms_message_delete_to_notifications() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL THEN
        DELETE FROM notification
        WHERE event_item_type = 'channel'
          AND event_item_id = NEW.channel_id::text
          AND metadata->>'messageId' = NEW.id::text;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: check_property_name_not_system(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.check_property_name_not_system() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.is_system = FALSE AND EXISTS (
        SELECT 1 FROM property_definitions 
        WHERE is_system = TRUE AND LOWER(display_name) = LOWER(NEW.display_name)
    ) THEN
        RAISE EXCEPTION 'Cannot create custom property with reserved system property name: %', NEW.display_name;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: delete_orphaned_property_definition(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.delete_orphaned_property_definition() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
    BEGIN
        IF NEW.team_id IS NULL AND NEW.user_id IS NULL AND NEW.is_system = FALSE THEN
            DELETE FROM property_definitions WHERE id = NEW.id;
            RETURN NULL;
        END IF;
        RETURN NEW;
    END;
$$;


--
-- Name: delete_share_permission_on_call_record_delete(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.delete_share_permission_on_call_record_delete() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  DELETE FROM "SharePermission" WHERE id = OLD.share_permission_id;
  RETURN OLD;
END;
$$;


--
-- Name: notify_user_notification_deletes(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_user_notification_deletes() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    affected_user_ids JSONB;
BEGIN
    SELECT jsonb_agg(user_id)
    INTO affected_user_ids
    FROM user_notification
    WHERE notification_id = OLD.id;

    IF affected_user_ids IS NOT NULL THEN
        BEGIN
            PERFORM pg_notify(
                'notification_events',
                jsonb_build_object(
                    'type', 'user_notification_deletes',
                    'notificationId', OLD.id,
                    'userIds', affected_user_ids
                )::TEXT
            );
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'pg_notify failed: %', SQLERRM;
        END;
    END IF;

    RETURN OLD;
END;
$$;


--
-- Name: set_crm_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.set_crm_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: Artifact; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Artifact" (
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    digest text NOT NULL,
    "messageId" text NOT NULL,
    "documentId" text,
    name text,
    "userId" text NOT NULL
);


--
-- Name: BlockedEmail; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."BlockedEmail" (
    email text NOT NULL
);


--
-- Name: BomPart; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."BomPart" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    sha text NOT NULL,
    path text NOT NULL,
    "documentBomId" bigint NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: ChannelSharePermission; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."ChannelSharePermission" (
    channel_id text NOT NULL,
    share_permission_id text NOT NULL,
    access_level public."AccessLevel" NOT NULL
);


--
-- Name: Chat; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Chat" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    "userId" text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "deletedAt" timestamp(3) without time zone,
    name text NOT NULL,
    model text DEFAULT 'gpt-4o'::text NOT NULL,
    "tokenCount" bigint,
    "projectId" text,
    "isPersistent" boolean DEFAULT false NOT NULL
);


--
-- Name: ChatAttachment; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."ChatAttachment" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    "old_attachmentType" text,
    "old_attachmentId" text,
    "chatId" text,
    "messageId" text,
    entity_type text NOT NULL,
    entity_id uuid NOT NULL
);


--
-- Name: ChatMessage; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."ChatMessage" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    content jsonb NOT NULL,
    role text NOT NULL,
    "chatId" text NOT NULL,
    model text
);


--
-- Name: ChatPermission; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."ChatPermission" (
    "chatId" text NOT NULL,
    "sharePermissionId" text NOT NULL
);


--
-- Name: Comment; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Comment" (
    id bigint NOT NULL,
    "threadId" bigint NOT NULL,
    owner text NOT NULL,
    sender text,
    text text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "deletedAt" timestamp(3) without time zone,
    "order" integer,
    metadata jsonb
);


--
-- Name: Comment_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."Comment_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: Comment_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."Comment_id_seq" OWNED BY public."Comment".id;


--
-- Name: Document; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Document" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    owner text NOT NULL,
    "fileType" text,
    "branchedFromId" text,
    "branchedFromVersionId" bigint,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "deletedAt" timestamp(3) without time zone,
    uploaded boolean DEFAULT false NOT NULL,
    "documentFamilyId" bigint,
    "projectId" text,
    "contentState" text DEFAULT 'pending'::text NOT NULL,
    "contentLocation" text,
    CONSTRAINT "Document_contentLocation_check" CHECK ((("contentLocation" IS NULL) OR ("contentLocation" = ANY (ARRAY['object_storage'::text, 'sync_service'::text, 'docx_bom_parts'::text, 'converted_pdf'::text, 'unknown'::text])))),
    CONSTRAINT "Document_contentState_check" CHECK (("contentState" = ANY (ARRAY['unknown'::text, 'pending'::text, 'ready'::text])))
);


--
-- Name: DocumentBom; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."DocumentBom" (
    id bigint NOT NULL,
    "revisionName" text,
    "documentId" text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: DocumentBom_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."DocumentBom_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: DocumentBom_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."DocumentBom_id_seq" OWNED BY public."DocumentBom".id;


--
-- Name: DocumentFamily; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."DocumentFamily" (
    id bigint NOT NULL,
    "rootDocumentId" text NOT NULL
);


--
-- Name: DocumentFamily_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."DocumentFamily_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: DocumentFamily_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."DocumentFamily_id_seq" OWNED BY public."DocumentFamily".id;


--
-- Name: DocumentInstance; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."DocumentInstance" (
    id bigint NOT NULL,
    "revisionName" text,
    "documentId" text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    sha text NOT NULL
);


--
-- Name: DocumentInstanceModificationData; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."DocumentInstanceModificationData" (
    id bigint NOT NULL,
    "documentInstanceId" bigint NOT NULL,
    "modificationData" jsonb NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "pdfPlaceableCommentMigratedAt" timestamp(3) without time zone,
    "pdfHighlightMigratedAt" timestamp(3) without time zone
);


--
-- Name: DocumentInstanceModificationData_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."DocumentInstanceModificationData_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: DocumentInstanceModificationData_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."DocumentInstanceModificationData_id_seq" OWNED BY public."DocumentInstanceModificationData".id;


--
-- Name: DocumentInstance_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."DocumentInstance_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: DocumentInstance_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."DocumentInstance_id_seq" OWNED BY public."DocumentInstance".id;


--
-- Name: DocumentPermission; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."DocumentPermission" (
    "documentId" text NOT NULL,
    "sharePermissionId" text NOT NULL
);


--
-- Name: DocumentProcessResult; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."DocumentProcessResult" (
    id bigint NOT NULL,
    "documentId" text NOT NULL,
    "jobType" text NOT NULL,
    content text NOT NULL
);


--
-- Name: DocumentProcessResult_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."DocumentProcessResult_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: DocumentProcessResult_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."DocumentProcessResult_id_seq" OWNED BY public."DocumentProcessResult".id;


--
-- Name: DocumentSummary; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."DocumentSummary" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    summary text NOT NULL,
    document_id text NOT NULL,
    version_id text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: DocumentText; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."DocumentText" (
    id bigint NOT NULL,
    content text NOT NULL,
    "documentId" text NOT NULL,
    "tokenCount" bigint DEFAULT 0 NOT NULL
);


--
-- Name: DocumentTextParts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."DocumentTextParts" (
    id text NOT NULL,
    reference text NOT NULL,
    "documentId" text NOT NULL
);


--
-- Name: DocumentText_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."DocumentText_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: DocumentText_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."DocumentText_id_seq" OWNED BY public."DocumentText".id;


--
-- Name: DocumentView; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."DocumentView" (
    id bigint NOT NULL,
    document_id text NOT NULL,
    user_id text,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: DocumentView_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."DocumentView_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: DocumentView_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."DocumentView_id_seq" OWNED BY public."DocumentView".id;


--
-- Name: EmailInsightsBackfillBatch; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."EmailInsightsBackfillBatch" (
    id text NOT NULL,
    "insightsBackfillJobId" text NOT NULL,
    "sqsMessageId" text,
    "threadIds" text[],
    "totalThreads" integer NOT NULL,
    status public.insights_backfill_batch_status DEFAULT 'Queued'::public.insights_backfill_batch_status NOT NULL,
    "insightsGeneratedCount" integer DEFAULT 0 NOT NULL,
    "insightIds" text[],
    "errorMessage" text,
    "queuedAt" timestamp(3) without time zone,
    "startedAt" timestamp(3) without time zone,
    "completedAt" timestamp(3) without time zone,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);


--
-- Name: EmailInsightsBackfillJob; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."EmailInsightsBackfillJob" (
    id text NOT NULL,
    "userId" text NOT NULL,
    "threadsProcessedCount" integer DEFAULT 0 NOT NULL,
    "insightsGeneratedCount" integer DEFAULT 0 NOT NULL,
    status public.insights_backfill_job_status DEFAULT 'Init'::public.insights_backfill_job_status NOT NULL,
    "completedAt" timestamp(3) without time zone,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);


--
-- Name: EmailThreadPermission; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."EmailThreadPermission" (
    "threadId" text NOT NULL,
    "sharePermissionId" text NOT NULL,
    "userId" text NOT NULL,
    "projectId" text
);


--
-- Name: EnterpriseEmailContacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."EnterpriseEmailContacts" (
    id integer NOT NULL,
    "organizationId" integer NOT NULL,
    "firstName" text NOT NULL,
    "lastName" text NOT NULL,
    email text NOT NULL
);


--
-- Name: EnterpriseEmailContacts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."EnterpriseEmailContacts_id_seq"
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: EnterpriseEmailContacts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."EnterpriseEmailContacts_id_seq" OWNED BY public."EnterpriseEmailContacts".id;


--
-- Name: EnterpriseIManageTenants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."EnterpriseIManageTenants" (
    id integer NOT NULL,
    "organizationId" integer NOT NULL,
    tenant_uri text NOT NULL,
    nickname text NOT NULL
);


--
-- Name: EnterpriseIManageTenants_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."EnterpriseIManageTenants_id_seq"
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: EnterpriseIManageTenants_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."EnterpriseIManageTenants_id_seq" OWNED BY public."EnterpriseIManageTenants".id;


--
-- Name: EnterpriseRules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."EnterpriseRules" (
    id integer NOT NULL,
    "organizationId" integer NOT NULL,
    "autoSetAsDefaultApp" boolean DEFAULT false NOT NULL,
    "disableAutoUpdate" boolean DEFAULT false NOT NULL,
    "setAsDefault" public."SetAsDefault" DEFAULT 'ASK'::public."SetAsDefault" NOT NULL,
    "setAsDefaultDocx" public."SetAsDefault" DEFAULT 'ASK'::public."SetAsDefault" NOT NULL
);


--
-- Name: EnterpriseRules_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."EnterpriseRules_id_seq"
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: EnterpriseRules_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."EnterpriseRules_id_seq" OWNED BY public."EnterpriseRules".id;


--
-- Name: Experiment; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Experiment" (
    id text NOT NULL,
    active boolean DEFAULT false NOT NULL,
    started_at timestamp(3) without time zone,
    ended_at timestamp(3) without time zone
);


--
-- Name: ExperimentLog; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."ExperimentLog" (
    experiment_id text NOT NULL,
    user_id text NOT NULL,
    "group" character varying(1) NOT NULL,
    completed boolean DEFAULT false NOT NULL
);


--
-- Name: HearthPrompt; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."HearthPrompt" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    title text NOT NULL,
    prompt text NOT NULL,
    icon text NOT NULL,
    color text NOT NULL,
    required_docs integer,
    user_id text NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: HearthPromptAttachment; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."HearthPromptAttachment" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    attachment_type text NOT NULL,
    attachment_id text NOT NULL,
    hearth_prompt_id text NOT NULL
);


--
-- Name: HearthPromptPermission; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."HearthPromptPermission" (
    hearth_prompt_id text NOT NULL,
    share_permission_id text NOT NULL
);


--
-- Name: InsightContext; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."InsightContext" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    "providerSource" text NOT NULL,
    "userId" text NOT NULL,
    "resourceId" text NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    consumed boolean DEFAULT false NOT NULL
);


--
-- Name: InstructionsDocuments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."InstructionsDocuments" (
    "documentId" text NOT NULL,
    "userId" text NOT NULL
);


--
-- Name: ItemLastAccessed; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."ItemLastAccessed" (
    item_id text NOT NULL,
    item_type text NOT NULL,
    last_accessed timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: JobToDocumentProcessResult; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."JobToDocumentProcessResult" (
    "jobId" text NOT NULL,
    "documentProcessResultId" bigint NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: Organization; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Organization" (
    id integer NOT NULL,
    name text NOT NULL,
    "emailToolDomain" text,
    "stripeCustomerId" text,
    status public."OrganizationStatus" DEFAULT 'PILOT'::public."OrganizationStatus" NOT NULL,
    seats integer,
    "allowListOnly" boolean,
    "llmProviders" text,
    "netDocumentsEnabled" boolean DEFAULT false NOT NULL
);


--
-- Name: OrganizationBilling; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."OrganizationBilling" (
    id integer NOT NULL,
    "organizationId" integer NOT NULL,
    email text NOT NULL
);


--
-- Name: OrganizationBilling_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."OrganizationBilling_id_seq"
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: OrganizationBilling_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."OrganizationBilling_id_seq" OWNED BY public."OrganizationBilling".id;


--
-- Name: OrganizationDefaultSharePermission; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."OrganizationDefaultSharePermission" (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    is_public boolean DEFAULT false NOT NULL,
    public_access_level text,
    organization_access_enabled boolean DEFAULT false NOT NULL,
    organization_access_level text
);


--
-- Name: OrganizationDefaultSharePermission_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."OrganizationDefaultSharePermission_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: OrganizationDefaultSharePermission_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."OrganizationDefaultSharePermission_id_seq" OWNED BY public."OrganizationDefaultSharePermission".id;


--
-- Name: OrganizationEmailMatches; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."OrganizationEmailMatches" (
    email character varying(100) NOT NULL,
    "organizationId" integer NOT NULL
);


--
-- Name: OrganizationIT; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."OrganizationIT" (
    email text NOT NULL,
    "organizationId" integer NOT NULL
);


--
-- Name: OrganizationInvitation; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."OrganizationInvitation" (
    id bigint NOT NULL,
    email text NOT NULL,
    organization_id integer NOT NULL
);


--
-- Name: OrganizationInvitation_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."OrganizationInvitation_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: OrganizationInvitation_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."OrganizationInvitation_id_seq" OWNED BY public."OrganizationInvitation".id;


--
-- Name: OrganizationItJob; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."OrganizationItJob" (
    id integer NOT NULL,
    "taskArn" text,
    "taskType" public."OrganizationItJobType" NOT NULL,
    "organizationId" integer NOT NULL
);


--
-- Name: OrganizationItJob_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."OrganizationItJob_id_seq"
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: OrganizationItJob_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."OrganizationItJob_id_seq" OWNED BY public."OrganizationItJob".id;


--
-- Name: OrganizationRetentionPolicy; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."OrganizationRetentionPolicy" (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    retention_days integer
);


--
-- Name: OrganizationRetentionPolicy_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."OrganizationRetentionPolicy_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: OrganizationRetentionPolicy_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."OrganizationRetentionPolicy_id_seq" OWNED BY public."OrganizationRetentionPolicy".id;


--
-- Name: Organization_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."Organization_id_seq"
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: Organization_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."Organization_id_seq" OWNED BY public."Organization".id;


--
-- Name: PdfHighlightAnchor; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."PdfHighlightAnchor" (
    uuid uuid DEFAULT gen_random_uuid() NOT NULL,
    "documentId" text NOT NULL,
    owner text NOT NULL,
    page integer NOT NULL,
    red integer NOT NULL,
    green integer NOT NULL,
    blue integer NOT NULL,
    alpha double precision NOT NULL,
    type integer NOT NULL,
    text text NOT NULL,
    "pageViewportWidth" double precision NOT NULL,
    "pageViewportHeight" double precision NOT NULL,
    "threadId" bigint,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "deletedAt" timestamp(3) without time zone
);


--
-- Name: PdfHighlightRect; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."PdfHighlightRect" (
    id bigint NOT NULL,
    top double precision NOT NULL,
    "left" double precision NOT NULL,
    width double precision NOT NULL,
    height double precision NOT NULL,
    "pdfHighlightAnchorId" uuid NOT NULL
);


--
-- Name: PdfHighlightRect_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."PdfHighlightRect_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: PdfHighlightRect_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."PdfHighlightRect_id_seq" OWNED BY public."PdfHighlightRect".id;


--
-- Name: PdfPlaceableCommentAnchor; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."PdfPlaceableCommentAnchor" (
    uuid uuid DEFAULT gen_random_uuid() NOT NULL,
    "documentId" text NOT NULL,
    owner text NOT NULL,
    "allowableEdits" jsonb,
    page integer NOT NULL,
    "wasEdited" boolean NOT NULL,
    "wasDeleted" boolean NOT NULL,
    "shouldLockOnSave" boolean NOT NULL,
    "originalPage" integer NOT NULL,
    "originalIndex" integer NOT NULL,
    "xPct" double precision NOT NULL,
    "yPct" double precision NOT NULL,
    "widthPct" double precision NOT NULL,
    "heightPct" double precision NOT NULL,
    rotation double precision NOT NULL,
    "threadId" bigint NOT NULL
);


--
-- Name: Permission; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Permission" (
    id text NOT NULL,
    description text NOT NULL
);


--
-- Name: Pin; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Pin" (
    "userId" text NOT NULL,
    "pinnedItemId" text NOT NULL,
    "pinnedItemType" text NOT NULL,
    "pinIndex" integer NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: Project; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Project" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    "userId" text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "parentId" text,
    "deletedAt" timestamp(3) without time zone,
    "uploadPending" boolean DEFAULT false NOT NULL,
    "uploadRequestId" text
);


--
-- Name: ProjectPermission; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."ProjectPermission" (
    "projectId" text NOT NULL,
    "sharePermissionId" text NOT NULL
);


--
-- Name: Role; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Role" (
    id text NOT NULL,
    description text NOT NULL
);


--
-- Name: RolesOnOrganizations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."RolesOnOrganizations" (
    "organizationId" integer NOT NULL,
    "roleId" text NOT NULL
);


--
-- Name: RolesOnPermissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."RolesOnPermissions" (
    "permissionId" text NOT NULL,
    "roleId" text NOT NULL
);


--
-- Name: RolesOnUsers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."RolesOnUsers" (
    "userId" text NOT NULL,
    "roleId" text NOT NULL
);


--
-- Name: SharePermission; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."SharePermission" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    "isPublic" boolean NOT NULL,
    "publicAccessLevel" text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: Thread; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."Thread" (
    id bigint NOT NULL,
    owner text NOT NULL,
    "documentId" text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "deletedAt" timestamp(3) without time zone,
    metadata jsonb,
    resolved boolean DEFAULT false NOT NULL
);


--
-- Name: ThreadAnchor; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."ThreadAnchor" (
    "threadId" bigint NOT NULL,
    "anchorId" uuid NOT NULL,
    "anchorTableName" public.anchor_table_name NOT NULL
);


--
-- Name: Thread_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."Thread_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: Thread_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."Thread_id_seq" OWNED BY public."Thread".id;


--
-- Name: UploadJob; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."UploadJob" (
    id bigint NOT NULL,
    "jobId" text NOT NULL,
    "jobType" text NOT NULL,
    "documentId" text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: UploadJob_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public."UploadJob_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: UploadJob_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public."UploadJob_id_seq" OWNED BY public."UploadJob".id;


--
-- Name: User; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."User" (
    id text NOT NULL,
    email text NOT NULL,
    name text,
    "group" character varying(1),
    "hasChromeExt" boolean DEFAULT false NOT NULL,
    "organizationId" integer,
    "stripeCustomerId" text,
    "tutorialComplete" boolean DEFAULT false NOT NULL,
    "hasOnboardingDocuments" boolean DEFAULT false NOT NULL,
    industry character varying(255),
    title character varying(20),
    "firstName" character varying(100),
    "lastName" character varying(100),
    "profilePicture" text,
    "profilePictureHash" character varying(40),
    hearth_user_id uuid NOT NULL,
    "aiDataConsent" boolean DEFAULT false NOT NULL,
    "createdAt" timestamp with time zone DEFAULT CURRENT_TIMESTAMP
);


--
-- Name: UserApiKey; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."UserApiKey" (
    key text NOT NULL,
    user_id text NOT NULL
);


--
-- Name: UserDocumentViewLocation; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."UserDocumentViewLocation" (
    user_id text NOT NULL,
    document_id text NOT NULL,
    location text NOT NULL,
    updated_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: UserHistory; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."UserHistory" (
    "userId" text NOT NULL,
    "itemId" text NOT NULL,
    "itemType" text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: UserInsightBatch; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."UserInsightBatch" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    "userId" text NOT NULL,
    "insightIds" text[],
    "totalChars" integer NOT NULL,
    "estimatedTokens" integer NOT NULL,
    "rankingContext" jsonb,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "expiresAt" timestamp(3) without time zone NOT NULL,
    version integer DEFAULT 1 NOT NULL
);


--
-- Name: UserInsights; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."UserInsights" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    content text NOT NULL,
    source text NOT NULL,
    "sourceLocation" jsonb,
    generated boolean DEFAULT true NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "spanStart" timestamp(3) without time zone,
    "spanEnd" timestamp(3) without time zone,
    confidence integer,
    "insightType" text,
    "relevanceKeywords" text[],
    "userId" text NOT NULL
);


--
-- Name: WebAnnotations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."WebAnnotations" (
    id text DEFAULT gen_random_uuid() NOT NULL,
    "messageId" text,
    url text NOT NULL,
    title text NOT NULL,
    publish_date timestamp(3) without time zone,
    description text,
    favicon_url text,
    image_url text,
    "chatId" text
);


--
-- Name: WebsocketConnectionPermissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."WebsocketConnectionPermissions" (
    "connectionId" text NOT NULL,
    "userId" text,
    permissions jsonb NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: _sqlx_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public._sqlx_migrations (
    version bigint NOT NULL,
    description text NOT NULL,
    installed_on timestamp with time zone DEFAULT now() NOT NULL,
    success boolean NOT NULL,
    checksum bytea NOT NULL,
    execution_time bigint NOT NULL
);


--
-- Name: account_merge_request; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.account_merge_request (
    id uuid NOT NULL,
    code text NOT NULL,
    hearth_user_id uuid NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    to_merge_hearth_user_id uuid NOT NULL
);


--
-- Name: action_execution_record; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.action_execution_record (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    action_id uuid NOT NULL,
    resource_id text,
    start_time timestamp with time zone NOT NULL,
    end_time timestamp with time zone NOT NULL,
    is_success boolean NOT NULL,
    result jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: active_streams; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.active_streams (
    entity_id text NOT NULL,
    stream_key text NOT NULL
);


--
-- Name: ai_pricing; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ai_pricing (
    model text NOT NULL,
    price_per_million_in real NOT NULL,
    price_per_million_out real NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: ai_projection; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ai_projection (
    id text NOT NULL,
    prompt text NOT NULL,
    prompt_hash text NOT NULL,
    target_type text NOT NULL,
    refresh_cadence text NOT NULL,
    expiry text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    model text,
    output_schema jsonb,
    CONSTRAINT ai_projection_expiry_check CHECK ((expiry = ANY (ARRAY['day'::text, 'week'::text, 'month'::text]))),
    CONSTRAINT ai_projection_refresh_cadence_check CHECK ((refresh_cadence = ANY (ARRAY['high'::text, 'medium'::text, 'low'::text]))),
    CONSTRAINT ai_projection_target_type_check CHECK ((target_type = ANY (ARRAY['user'::text, 'team'::text])))
);


--
-- Name: ai_usage; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ai_usage (
    id uuid NOT NULL,
    feature text NOT NULL,
    user_id text NOT NULL,
    entity uuid,
    model text NOT NULL,
    input_tokens bigint NOT NULL,
    output_tokens bigint NOT NULL,
    price_per_million_in real,
    price_per_million_out real,
    total real,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: bot_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_tokens (
    id uuid NOT NULL,
    bot_id uuid NOT NULL,
    label text,
    last_used_at timestamp with time zone,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    token text NOT NULL
);


--
-- Name: bots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bots (
    id uuid NOT NULL,
    kind text NOT NULL,
    owner_user_id text,
    team_id uuid,
    name text NOT NULL,
    handle text NOT NULL,
    description text,
    avatar_url text,
    created_by text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    CONSTRAINT bots_kind_check CHECK ((kind = ANY (ARRAY['owned'::text, 'system'::text]))),
    CONSTRAINT bots_kind_owner_check CHECK ((((kind = 'owned'::text) AND ((((owner_user_id IS NOT NULL))::integer + ((team_id IS NOT NULL))::integer) = 1)) OR ((kind = 'system'::text) AND (owner_user_id IS NULL) AND (team_id IS NULL))))
);


--
-- Name: call_participants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.call_participants (
    call_id uuid NOT NULL,
    user_id text NOT NULL,
    joined_at timestamp with time zone DEFAULT now() NOT NULL,
    left_at timestamp with time zone
);


--
-- Name: call_record_participants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.call_record_participants (
    call_record_id uuid NOT NULL,
    user_id text NOT NULL,
    joined_at timestamp with time zone NOT NULL,
    left_at timestamp with time zone
);


--
-- Name: call_record_transcripts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.call_record_transcripts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    call_record_id uuid NOT NULL,
    segment_id text,
    speaker_id text NOT NULL,
    content text NOT NULL,
    started_at timestamp with time zone NOT NULL,
    ended_at timestamp with time zone,
    sequence_num integer NOT NULL,
    diarized_speaker_id text,
    custom_speaker text,
    voice_id uuid
);


--
-- Name: call_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.call_records (
    id uuid NOT NULL,
    channel_id uuid NOT NULL,
    room_name text NOT NULL,
    created_by text NOT NULL,
    started_at timestamp with time zone NOT NULL,
    ended_at timestamp with time zone DEFAULT now() NOT NULL,
    duration_ms bigint NOT NULL,
    egress_id text,
    share_permission_id text NOT NULL,
    recording_key text,
    summary text,
    custom_name text,
    recording_started_at timestamp with time zone,
    share_with_team boolean DEFAULT true NOT NULL,
    preview_url text
);


--
-- Name: call_transcripts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.call_transcripts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    call_id uuid NOT NULL,
    segment_id text NOT NULL,
    speaker_id text NOT NULL,
    content text NOT NULL,
    started_at timestamp with time zone NOT NULL,
    ended_at timestamp with time zone,
    sequence_num integer NOT NULL,
    diarized_speaker_id text,
    voice_id uuid
);


--
-- Name: calls; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.calls (
    id uuid NOT NULL,
    channel_id uuid NOT NULL,
    room_name text NOT NULL,
    created_by text NOT NULL,
    egress_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    share_permission_id text NOT NULL,
    recording_key text,
    share_with_team boolean DEFAULT true NOT NULL,
    recording_started_at timestamp with time zone,
    preview_url text
);


--
-- Name: channel_notification_email_sent; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_notification_email_sent (
    channel_id uuid NOT NULL,
    user_id text NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: comms_activity; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.comms_activity (
    id uuid NOT NULL,
    user_id text NOT NULL,
    channel_id uuid NOT NULL,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    updated_at timestamp without time zone DEFAULT now() NOT NULL,
    viewed_at timestamp without time zone,
    interacted_at timestamp without time zone
);


--
-- Name: comms_attachments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.comms_attachments (
    id uuid NOT NULL,
    message_id uuid NOT NULL,
    entity_type character varying(32) NOT NULL,
    entity_id character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    channel_id uuid NOT NULL,
    width integer,
    height integer
);


--
-- Name: comms_channel_participants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.comms_channel_participants (
    channel_id uuid NOT NULL,
    role public.comms_participant_role NOT NULL,
    user_id text NOT NULL,
    joined_at timestamp with time zone DEFAULT now() NOT NULL,
    left_at timestamp with time zone
);


--
-- Name: comms_channels; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.comms_channels (
    id uuid NOT NULL,
    name character varying(255),
    channel_type public.comms_channel_type NOT NULL,
    org_id bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    owner_id text NOT NULL,
    team_id uuid,
    join_code uuid,
    auto_join_team boolean DEFAULT false NOT NULL,
    CONSTRAINT no_channel_org_id CHECK ((org_id IS NULL)),
    CONSTRAINT valid_channel_name CHECK ((((channel_type = 'direct_message'::public.comms_channel_type) AND (name IS NULL)) OR ((channel_type = ANY (ARRAY['public'::public.comms_channel_type, 'team'::public.comms_channel_type])) AND (name IS NOT NULL)) OR (channel_type = 'private'::public.comms_channel_type))),
    CONSTRAINT valid_team_channel CHECK ((((channel_type = 'team'::public.comms_channel_type) AND (team_id IS NOT NULL)) OR ((channel_type <> 'team'::public.comms_channel_type) AND (team_id IS NULL))))
);


--
-- Name: comms_entity_mentions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.comms_entity_mentions (
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    entity_type character varying(32) NOT NULL,
    entity_id character varying NOT NULL,
    source_entity_type character varying(32) NOT NULL,
    source_entity_id character varying NOT NULL,
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id character varying
);


--
-- Name: comms_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.comms_messages (
    id uuid NOT NULL,
    channel_id uuid NOT NULL,
    thread_id uuid,
    sender_id text NOT NULL,
    content text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    edited_at timestamp without time zone,
    deleted_at timestamp without time zone,
    triggered_by_user_id text
);


--
-- Name: comms_reactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.comms_reactions (
    message_id uuid NOT NULL,
    emoji character varying(32) NOT NULL,
    user_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: contacts_backfill_outbox; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.contacts_backfill_outbox (
    id integer NOT NULL,
    comms_channel_id uuid NOT NULL,
    user_ids jsonb NOT NULL,
    applied_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: contacts_backfill_outbox_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.contacts_backfill_outbox_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: contacts_backfill_outbox_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.contacts_backfill_outbox_id_seq OWNED BY public.contacts_backfill_outbox.id;


--
-- Name: contacts_connections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.contacts_connections (
    id integer NOT NULL,
    user1 text NOT NULL,
    user2 text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT contacts_connections_user1_user2_check CHECK ((user1 < (user2 COLLATE "C")))
);


--
-- Name: contacts_connections_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.contacts_connections_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: contacts_connections_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.contacts_connections_id_seq OWNED BY public.contacts_connections.id;


--
-- Name: crm_cleanup_candidates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.crm_cleanup_candidates (
    id bigint NOT NULL,
    link_id uuid NOT NULL,
    contact_email text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: crm_cleanup_candidates_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.crm_cleanup_candidates ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.crm_cleanup_candidates_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: crm_cleanup_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.crm_cleanup_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    status public.crm_cleanup_job_status DEFAULT 'Init'::public.crm_cleanup_job_status NOT NULL,
    total_candidates bigint NOT NULL,
    dispatched_count bigint DEFAULT 0 NOT NULL,
    max_candidate_id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: crm_comment; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.crm_comment (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    thread_id uuid NOT NULL,
    owner text NOT NULL,
    sender text,
    text text NOT NULL,
    "order" integer,
    metadata jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone
);


--
-- Name: crm_companies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.crm_companies (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    team_id uuid NOT NULL,
    email_sync boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    hidden boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    first_interaction timestamp with time zone NOT NULL,
    last_interaction timestamp with time zone NOT NULL,
    custom_name text,
    manually_created boolean DEFAULT false NOT NULL
);


--
-- Name: crm_contact_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.crm_contact_sources (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    contact_id uuid NOT NULL,
    link_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: crm_contacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.crm_contacts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    email text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    name text,
    hidden boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    first_interaction timestamp with time zone NOT NULL,
    last_interaction timestamp with time zone NOT NULL,
    manually_created boolean DEFAULT false NOT NULL
);


--
-- Name: crm_domain_directory; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.crm_domain_directory (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    domain text NOT NULL,
    name text,
    description text,
    icon_url text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    apollo_organization_id text,
    website_url text,
    linkedin_url text,
    twitter_url text,
    facebook_url text,
    industry text,
    keywords text[],
    technologies text[],
    estimated_num_employees integer,
    annual_revenue bigint,
    annual_revenue_printed text,
    total_funding bigint,
    total_funding_printed text,
    latest_funding_stage text,
    latest_funding_round_date timestamp with time zone,
    founded_year integer,
    publicly_traded_symbol text,
    publicly_traded_exchange text,
    phone text,
    raw_address text,
    street_address text,
    city text,
    state text,
    postal_code text,
    country text,
    raw jsonb,
    enriched_at timestamp with time zone
);


--
-- Name: crm_domains; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.crm_domains (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    domain text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid NOT NULL
);


--
-- Name: crm_thread; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.crm_thread (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid,
    contact_id uuid,
    owner text NOT NULL,
    resolved boolean DEFAULT false NOT NULL,
    metadata jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    CONSTRAINT crm_thread_one_parent CHECK ((num_nonnulls(company_id, contact_id) = 1))
);


--
-- Name: document_email; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.document_email (
    document_id text NOT NULL,
    email_attachment_id uuid NOT NULL
);


--
-- Name: document_sub_type; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.document_sub_type (
    document_id text NOT NULL,
    sub_type public.document_sub_type_value NOT NULL
);


--
-- Name: email_attachments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_attachments (
    id uuid NOT NULL,
    message_id uuid NOT NULL,
    provider_attachment_id text,
    filename character varying(512),
    mime_type character varying(255),
    size_bytes bigint,
    content_id character varying(255),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    upload_claimed_at timestamp with time zone
);


--
-- Name: email_attachments_drafts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_attachments_drafts (
    id uuid NOT NULL,
    draft_id uuid NOT NULL,
    file_name text NOT NULL,
    content_type text NOT NULL,
    sha text NOT NULL,
    size integer NOT NULL,
    s3_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: email_attachments_fwd; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_attachments_fwd (
    message_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: email_attachments_sfs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_attachments_sfs (
    id uuid NOT NULL,
    attachment_id uuid,
    sfs_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: email_backfill_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_backfill_jobs (
    id uuid NOT NULL,
    link_id uuid,
    threads_requested_limit integer,
    total_threads integer DEFAULT 0 NOT NULL,
    status public.email_backfill_job_status DEFAULT 'Init'::public.email_backfill_job_status NOT NULL,
    threads_retrieved_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    fusionauth_user_id text NOT NULL
);


--
-- Name: email_contacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_contacts (
    id uuid NOT NULL,
    link_id uuid NOT NULL,
    email_address character varying(320) NOT NULL,
    name character varying(255),
    original_photo_url text,
    sfs_photo_url text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: email_filters; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_filters (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    link_id uuid NOT NULL,
    email_address character varying(320),
    email_domain character varying(255),
    is_important boolean NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT email_filters_address_xor_domain_chk CHECK ((((email_address IS NOT NULL) AND (TRIM(BOTH FROM email_address) <> ''::text)) <> ((email_domain IS NOT NULL) AND (TRIM(BOTH FROM email_domain) <> ''::text)))),
    CONSTRAINT email_filters_email_domain_format_chk CHECK (((email_domain IS NULL) OR ((TRIM(BOTH FROM email_domain) <> ''::text) AND (POSITION(('@'::text) IN (email_domain)) = 0))))
);


--
-- Name: email_gmail_histories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_gmail_histories (
    link_id uuid NOT NULL,
    history_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: email_labels; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_labels (
    id uuid NOT NULL,
    link_id uuid NOT NULL,
    provider_label_id text NOT NULL,
    name character varying(255) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    message_list_visibility public.email_message_list_visibility_enum DEFAULT 'Show'::public.email_message_list_visibility_enum NOT NULL,
    label_list_visibility public.email_label_list_visibility_enum DEFAULT 'LabelShow'::public.email_label_list_visibility_enum NOT NULL,
    type public.email_label_type_enum DEFAULT 'User'::public.email_label_type_enum NOT NULL
);


--
-- Name: email_links; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_links (
    id uuid NOT NULL,
    hearth_id text NOT NULL,
    fusionauth_user_id text NOT NULL,
    email_address character varying(320) NOT NULL,
    provider public.email_user_provider_enum NOT NULL,
    is_sync_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    needs_reauth boolean DEFAULT false NOT NULL,
    last_sync_error_at timestamp with time zone,
    is_primary boolean GENERATED ALWAYS AS ((hearth_id = ('hearth|'::text || lower((email_address)::text)))) STORED NOT NULL
);


--
-- Name: email_links_history; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_links_history (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    link_id uuid NOT NULL,
    fusionauth_user_id text NOT NULL,
    email_address character varying(320) NOT NULL,
    provider public.email_user_provider_enum NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    deletion_reason text
);


--
-- Name: email_message_labels; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_message_labels (
    message_id uuid NOT NULL,
    label_id uuid NOT NULL
);


--
-- Name: email_message_recipients; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_message_recipients (
    message_id uuid NOT NULL,
    contact_id uuid NOT NULL,
    recipient_type public.email_recipient_type NOT NULL,
    name character varying(255)
)
WITH (autovacuum_vacuum_cost_delay='10', autovacuum_vacuum_cost_limit='200', autovacuum_vacuum_scale_factor='0.02', autovacuum_vacuum_threshold='5000', autovacuum_analyze_scale_factor='0.01', autovacuum_analyze_threshold='5000');


--
-- Name: email_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_messages (
    id uuid NOT NULL,
    provider_id text,
    thread_id uuid NOT NULL,
    provider_thread_id text,
    link_id uuid NOT NULL,
    provider_history_id text,
    internal_date_ts timestamp with time zone,
    snippet text,
    size_estimate bigint,
    subject text,
    from_contact_id uuid,
    sent_at timestamp with time zone,
    has_attachments boolean DEFAULT false NOT NULL,
    is_read boolean DEFAULT false NOT NULL,
    is_starred boolean DEFAULT false NOT NULL,
    is_sent boolean DEFAULT false NOT NULL,
    is_draft boolean DEFAULT false NOT NULL,
    body_text text,
    body_html_sanitized text,
    body_hearth text,
    headers_jsonb jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    global_id text,
    replying_to_id uuid,
    from_name character varying(255)
);


--
-- Name: email_scheduled_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_scheduled_messages (
    link_id uuid NOT NULL,
    message_id uuid NOT NULL,
    send_time timestamp with time zone NOT NULL,
    sent boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    processing boolean DEFAULT false NOT NULL
);


--
-- Name: email_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_settings (
    link_id uuid NOT NULL,
    signature_on_replies_forwards boolean DEFAULT false NOT NULL,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    updated_at timestamp without time zone DEFAULT now() NOT NULL,
    signature text
);


--
-- Name: email_sfs_mappings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_sfs_mappings (
    source text NOT NULL,
    destination text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: email_sync_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_sync_tokens (
    link_id uuid NOT NULL,
    contacts_sync_token character varying(255),
    other_contacts_sync_token character varying(255)
);


--
-- Name: email_threads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_threads (
    id uuid NOT NULL,
    provider_id text,
    link_id uuid NOT NULL,
    inbox_visible boolean DEFAULT false NOT NULL,
    is_read boolean DEFAULT false NOT NULL,
    latest_inbound_message_ts timestamp with time zone,
    latest_outbound_message_ts timestamp with time zone,
    latest_non_spam_message_ts timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    project_id text,
    has_calendar_attachment boolean DEFAULT false NOT NULL,
    is_signal boolean DEFAULT false NOT NULL
);


--
-- Name: email_user_history; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_user_history (
    link_id uuid NOT NULL,
    thread_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: entity_access; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_access (
    id bigint NOT NULL,
    entity_id uuid NOT NULL,
    entity_type text NOT NULL,
    source_id text NOT NULL,
    source_type public.entity_access_source_type NOT NULL,
    access_level public."AccessLevel" NOT NULL,
    granted_from_project_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: entity_access_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.entity_access_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: entity_access_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.entity_access_id_seq OWNED BY public.entity_access.id;


--
-- Name: entity_properties; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_properties (
    id uuid NOT NULL,
    entity_id text NOT NULL,
    entity_type public.property_entity_type NOT NULL,
    property_definition_id uuid NOT NULL,
    "values" jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT check_values_structure CHECK ((("values" IS NULL) OR ((("values" ->> 'type'::text) = ANY (ARRAY['Boolean'::text, 'Number'::text, 'String'::text, 'Date'::text])) AND (jsonb_typeof(("values" -> 'value'::text)) <> 'array'::text)) OR ((("values" ->> 'type'::text) = ANY (ARRAY['SelectOption'::text, 'EntityReference'::text, 'Link'::text])) AND (jsonb_typeof(("values" -> 'value'::text)) = 'array'::text))))
);


--
-- Name: excluded_default_view; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.excluded_default_view (
    id uuid NOT NULL,
    user_id text NOT NULL,
    default_view_id text NOT NULL
);


--
-- Name: favorite; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.favorite (
    user_id text NOT NULL,
    entity_type text NOT NULL,
    entity_id text NOT NULL,
    sort_order double precision DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: foreign_entity; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.foreign_entity (
    id uuid NOT NULL,
    foreign_entity_id text NOT NULL,
    foreign_entity_source text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    stored_for_id text NOT NULL,
    stored_for_auth_entity text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: frecency_aggregates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.frecency_aggregates (
    id bigint NOT NULL,
    entity_id text NOT NULL,
    entity_type text NOT NULL,
    user_id text NOT NULL,
    event_count integer DEFAULT 0 NOT NULL,
    frecency_score double precision DEFAULT 0.0 NOT NULL,
    first_event timestamp with time zone NOT NULL,
    recent_events jsonb DEFAULT '[]'::jsonb NOT NULL
);


--
-- Name: frecency_aggregates_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.frecency_aggregates_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: frecency_aggregates_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.frecency_aggregates_id_seq OWNED BY public.frecency_aggregates.id;


--
-- Name: frecency_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.frecency_events (
    id bigint NOT NULL,
    user_id text NOT NULL,
    entity_type text NOT NULL,
    event_type text NOT NULL,
    "timestamp" timestamp with time zone NOT NULL,
    connection_id text NOT NULL,
    entity_id text NOT NULL,
    was_processed boolean DEFAULT false NOT NULL
);


--
-- Name: frecency_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.frecency_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: frecency_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.frecency_events_id_seq OWNED BY public.frecency_events.id;


--
-- Name: github_app_installation; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.github_app_installation (
    id text NOT NULL,
    source_id text NOT NULL,
    source_type public.github_app_installation_source_type NOT NULL
);


--
-- Name: github_app_installation_installer; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.github_app_installation_installer (
    installation_id text NOT NULL,
    github_user_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: github_links; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.github_links (
    id uuid NOT NULL,
    hearth_id text NOT NULL,
    fusionauth_user_id uuid NOT NULL,
    github_username character varying(255) NOT NULL,
    github_user_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: github_pr_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.github_pr_tasks (
    id uuid NOT NULL,
    github_key text NOT NULL,
    task_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    team_id uuid
);


--
-- Name: hearth_user; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hearth_user (
    id uuid NOT NULL,
    username text NOT NULL,
    email text NOT NULL,
    stripe_customer_id text NOT NULL,
    has_trialed boolean DEFAULT true NOT NULL
);


--
-- Name: hearth_user_email_verification; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hearth_user_email_verification (
    hearth_user_id uuid NOT NULL,
    email text NOT NULL,
    is_verified boolean DEFAULT false NOT NULL
);


--
-- Name: hearth_user_info; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hearth_user_info (
    hearth_user_id uuid NOT NULL,
    industry text,
    title text,
    first_name text,
    last_name text,
    profile_picture text,
    profile_picture_hash character varying(40)
);


--
-- Name: hearth_user_links; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hearth_user_links (
    primary_hearth_id text NOT NULL,
    child_hearth_id text NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    link_id uuid NOT NULL,
    CONSTRAINT hearth_user_links_check CHECK ((primary_hearth_id <> child_hearth_id))
);


--
-- Name: hearth_user_voice; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hearth_user_voice (
    hearth_user_id uuid NOT NULL,
    voice_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: id_mapping; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.id_mapping (
    source_id text NOT NULL,
    target_id text NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: import_entity; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.import_entity (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id text NOT NULL,
    team_id uuid,
    source text NOT NULL,
    foreign_id text NOT NULL,
    status text DEFAULT 'staged'::text NOT NULL,
    initiator text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    entity_id text,
    entity_type text,
    last_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT import_entity_check CHECK (((status <> 'imported'::text) OR (entity_id IS NOT NULL))),
    CONSTRAINT import_entity_initiator_check CHECK ((initiator = ANY (ARRAY['onboarding'::text, 'chat'::text]))),
    CONSTRAINT import_entity_source_check CHECK ((source = ANY (ARRAY['linear'::text, 'notion'::text, 'slack'::text]))),
    CONSTRAINT import_entity_status_check CHECK ((status = ANY (ARRAY['staged'::text, 'importing'::text, 'imported'::text, 'discarded'::text])))
);


--
-- Name: import_run; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.import_run (
    user_id text NOT NULL,
    source text NOT NULL,
    status public.import_run_status NOT NULL,
    error text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    auto_import boolean DEFAULT false NOT NULL,
    CONSTRAINT import_run_source_check CHECK ((source = ANY (ARRAY['linear'::text, 'notion'::text, 'slack'::text])))
);


--
-- Name: in_progress_email_link; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.in_progress_email_link (
    id uuid NOT NULL,
    email text NOT NULL,
    hearth_user_id uuid NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: in_progress_user_link; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.in_progress_user_link (
    id uuid NOT NULL,
    hearth_user_id uuid NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    linked_email text
);


--
-- Name: mcp_servers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.mcp_servers (
    user_id text NOT NULL,
    url text NOT NULL,
    server_name text NOT NULL,
    credentials bytea,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: memory; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.memory (
    id uuid NOT NULL,
    user_id text NOT NULL,
    memory text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: mobile_welcome_email; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.mobile_welcome_email (
    email text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: notification; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification (
    id uuid NOT NULL,
    notification_event_type character varying(255) NOT NULL,
    event_item_id text NOT NULL,
    event_item_type text NOT NULL,
    service_sender text NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    sender_id text,
    apns_collapse_key text,
    secondary_event_item_id text,
    secondary_event_item_type text,
    CONSTRAINT notification_secondary_event_item_pair_check CHECK (((secondary_event_item_id IS NULL) = (secondary_event_item_type IS NULL)))
);


--
-- Name: COLUMN notification.secondary_event_item_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.notification.secondary_event_item_id IS 'id of an optional secondary entity related to the notification';


--
-- Name: COLUMN notification.secondary_event_item_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.notification.secondary_event_item_type IS 'type of an optional secondary entity related to the notification';


--
-- Name: notification_email_sent; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_email_sent (
    user_id text NOT NULL,
    sent_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: notification_email_unsubscribe; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_email_unsubscribe (
    email text NOT NULL
);


--
-- Name: notification_email_unsubscribe_code; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_email_unsubscribe_code (
    email text NOT NULL,
    code uuid NOT NULL
);


--
-- Name: notification_message_receipt; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_message_receipt (
    message_id text NOT NULL,
    user_id text NOT NULL,
    notification_id uuid NOT NULL,
    failed boolean DEFAULT false NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    failed_at timestamp without time zone
);


--
-- Name: notification_user_device_registration; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_user_device_registration (
    id uuid NOT NULL,
    user_id text NOT NULL,
    device_token text NOT NULL,
    device_endpoint text NOT NULL,
    device_type public.notification_device_type_option NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    last_used_at timestamp without time zone
);


--
-- Name: processing_ai_projections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.processing_ai_projections (
    ai_projection_id text NOT NULL,
    target_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: promoted_shared_mailboxes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promoted_shared_mailboxes (
    hearth_id text NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: property_definitions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.property_definitions (
    id uuid NOT NULL,
    user_id text,
    display_name text NOT NULL,
    data_type public.property_data_type NOT NULL,
    is_multi_select boolean NOT NULL,
    specific_entity_type public.property_entity_type,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    is_system boolean DEFAULT false NOT NULL,
    team_id uuid,
    CONSTRAINT owned_by_team_or_user_or_system CHECK (((((is_system)::integer + ((team_id IS NOT NULL))::integer) + ((user_id IS NOT NULL))::integer) = 1))
);


--
-- Name: property_options; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.property_options (
    id uuid NOT NULL,
    property_definition_id uuid NOT NULL,
    display_order integer DEFAULT 0 NOT NULL,
    number_value double precision,
    string_value text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    color text,
    CONSTRAINT check_option_value_set CHECK ((((number_value IS NOT NULL) AND (string_value IS NULL)) OR ((number_value IS NULL) AND (string_value IS NOT NULL)))),
    CONSTRAINT property_options_color_hex CHECK (((color IS NULL) OR (color ~ '^#[0-9A-Fa-f]{6}$'::text)))
);


--
-- Name: referral_tracking; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.referral_tracking (
    id uuid NOT NULL,
    referrer_id uuid NOT NULL,
    referred_id uuid NOT NULL,
    status text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT referral_tracking_no_self_referral CHECK ((referrer_id <> referred_id))
);


--
-- Name: resolved_message_content; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resolved_message_content (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    "messageId" text NOT NULL,
    content jsonb NOT NULL,
    "createdAt" timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: saved_view; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.saved_view (
    id uuid NOT NULL,
    user_id text NOT NULL,
    name text NOT NULL,
    config jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone NOT NULL
);


--
-- Name: scheduled_action; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scheduled_action (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner text NOT NULL,
    name text NOT NULL,
    schedule text NOT NULL,
    kind text NOT NULL,
    timezone text NOT NULL,
    task jsonb NOT NULL,
    claimed timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    next_run_at timestamp with time zone NOT NULL,
    enabled boolean NOT NULL
);


--
-- Name: task_duplicate_embedding; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.task_duplicate_embedding (
    document_id text NOT NULL,
    content text NOT NULL,
    embedding public.vector(1536) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    search_key text NOT NULL
);


--
-- Name: task_duplicate_match; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.task_duplicate_match (
    id uuid NOT NULL,
    task_id text NOT NULL,
    duplicate_task_id text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    vector_score double precision NOT NULL,
    judge_model text,
    judge_reason text,
    dismissed_by text,
    dismissed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT task_duplicate_match_order CHECK ((task_id < duplicate_task_id)),
    CONSTRAINT task_duplicate_match_status CHECK ((status = ANY (ARRAY['active'::text, 'dismissed'::text])))
);


--
-- Name: team; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team (
    id uuid NOT NULL,
    name text NOT NULL,
    owner_id text NOT NULL,
    subscription_id text,
    seat_count integer DEFAULT 0 NOT NULL,
    plan public.team_plan,
    slug character varying(20) DEFAULT 'MACRO'::character varying NOT NULL,
    paying boolean DEFAULT false NOT NULL,
    auto_join_domain text,
    enterprise boolean DEFAULT false NOT NULL,
    allow_non_admin_invites boolean DEFAULT true NOT NULL,
    CONSTRAINT team_slug_format_check CHECK (((slug)::text ~ '^[A-Z]+(_[A-Z]+)*$'::text))
);


--
-- Name: team_crm_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_crm_settings (
    team_id uuid NOT NULL,
    crm_enabled boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    edit_stages_role public.team_role DEFAULT 'admin'::public.team_role NOT NULL,
    move_closed_deals_role public.team_role DEFAULT 'admin'::public.team_role NOT NULL,
    delete_records_role public.team_role DEFAULT 'admin'::public.team_role NOT NULL,
    closed_stage_ids uuid[],
    team_views jsonb DEFAULT '[]'::jsonb NOT NULL,
    default_team_view_id text
);


--
-- Name: COLUMN team_crm_settings.closed_stage_ids; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.team_crm_settings.closed_stage_ids IS 'Stage option ids counting as closed deals; NULL = label heuristic on the client';


--
-- Name: COLUMN team_crm_settings.team_views; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.team_crm_settings.team_views IS 'Opaque array of team saved views, owned by the frontend';


--
-- Name: team_invite; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_invite (
    id uuid NOT NULL,
    email text NOT NULL,
    team_id uuid NOT NULL,
    team_role public.team_role DEFAULT 'member'::public.team_role NOT NULL,
    invited_by text NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    last_sent_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: team_task; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_task (
    team_id uuid NOT NULL,
    document_id text NOT NULL,
    task_num integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT team_task_num_positive CHECK ((task_num > 0))
);


--
-- Name: team_task_counter; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_task_counter (
    team_id uuid NOT NULL,
    last_task_num integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT team_task_counter_positive CHECK ((last_task_num >= 0))
);


--
-- Name: team_user; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_user (
    user_id text NOT NULL,
    team_id uuid NOT NULL,
    team_role public.team_role NOT NULL
);


--
-- Name: user_ai_projection; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_ai_projection (
    ai_projection_id text NOT NULL,
    target_id text NOT NULL,
    prompt_hash text NOT NULL,
    status text DEFAULT 'cold'::text NOT NULL,
    result text,
    error text,
    generated_at timestamp with time zone,
    stale_at timestamp with time zone,
    last_requested_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_ai_projection_status_check CHECK ((status = ANY (ARRAY['loading'::text, 'cold'::text, 'ready'::text, 'refreshing'::text, 'error'::text])))
);


--
-- Name: user_mute_notification; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_mute_notification (
    user_id text NOT NULL
);


--
-- Name: user_notification; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_notification (
    user_id text NOT NULL,
    notification_id uuid NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    sent boolean DEFAULT false NOT NULL,
    seen_at timestamp without time zone,
    deleted_at timestamp without time zone,
    done boolean DEFAULT false NOT NULL,
    is_important_v0 boolean DEFAULT false NOT NULL
);


--
-- Name: user_notification_item_unsubscribe; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_notification_item_unsubscribe (
    user_id text NOT NULL,
    item_id text NOT NULL,
    item_type text NOT NULL
);


--
-- Name: user_notification_type_preference; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_notification_type_preference (
    user_id text NOT NULL,
    notification_event_type character varying(255) NOT NULL
);


--
-- Name: user_onboarding; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_onboarding (
    user_id text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    skipped boolean DEFAULT false NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_onboarding_status_check CHECK ((status = ANY (ARRAY['active'::text, 'completed'::text])))
);


--
-- Name: voice; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.voice (
    id uuid NOT NULL,
    embedding public.vector(256) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: webhook; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhook (
    id text NOT NULL,
    workspace_id text NOT NULL,
    owner_user_id text,
    owner_bot_id text,
    name text NOT NULL,
    endpoint_url text NOT NULL,
    signing_secret text NOT NULL,
    headers jsonb DEFAULT '{}'::jsonb NOT NULL,
    filters jsonb NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    is_valid boolean DEFAULT false NOT NULL,
    paused_at timestamp with time zone,
    pause_reason text,
    last_success_at timestamp with time zone,
    last_failure_at timestamp with time zone,
    created_by_user_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    CONSTRAINT webhook_filters_is_array CHECK ((jsonb_typeof(filters) = 'array'::text)),
    CONSTRAINT webhook_status CHECK ((status = ANY (ARRAY['active'::text, 'paused'::text, 'disabled'::text])))
);


--
-- Name: webhook_delivery; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhook_delivery (
    id text NOT NULL,
    webhook_id text NOT NULL,
    event_id text NOT NULL,
    event text NOT NULL,
    event_schema_version integer NOT NULL,
    event_occurred_at timestamp with time zone NOT NULL,
    event_entity_type text NOT NULL,
    event_entity_id text NOT NULL,
    event_ordering_key text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    request_url text NOT NULL,
    request_headers_redacted jsonb DEFAULT '{}'::jsonb NOT NULL,
    request_body jsonb NOT NULL,
    next_attempt_at timestamp with time zone,
    attempt_count integer DEFAULT 0 NOT NULL,
    first_attempt_at timestamp with time zone,
    last_attempt_at timestamp with time zone,
    delivered_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT webhook_delivery_attempt_count_non_negative CHECK ((attempt_count >= 0)),
    CONSTRAINT webhook_delivery_attempt_window_valid CHECK (((first_attempt_at IS NULL) OR (last_attempt_at IS NULL) OR (last_attempt_at >= first_attempt_at)))
);


--
-- Name: webhook_delivery_attempt; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhook_delivery_attempt (
    id text NOT NULL,
    webhook_delivery_id text NOT NULL,
    attempt_number integer NOT NULL,
    status text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    duration_ms integer,
    response_status integer,
    response_headers_redacted jsonb,
    response_body_preview text,
    error_kind text,
    error_message text,
    CONSTRAINT webhook_delivery_attempt_completed_after_started CHECK (((completed_at IS NULL) OR (completed_at >= started_at))),
    CONSTRAINT webhook_delivery_attempt_duration_non_negative CHECK (((duration_ms IS NULL) OR (duration_ms >= 0))),
    CONSTRAINT webhook_delivery_attempt_number_non_negative CHECK ((attempt_number >= 0))
);


--
-- Name: Comment id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Comment" ALTER COLUMN id SET DEFAULT nextval('public."Comment_id_seq"'::regclass);


--
-- Name: DocumentBom id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentBom" ALTER COLUMN id SET DEFAULT nextval('public."DocumentBom_id_seq"'::regclass);


--
-- Name: DocumentFamily id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentFamily" ALTER COLUMN id SET DEFAULT nextval('public."DocumentFamily_id_seq"'::regclass);


--
-- Name: DocumentInstance id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentInstance" ALTER COLUMN id SET DEFAULT nextval('public."DocumentInstance_id_seq"'::regclass);


--
-- Name: DocumentInstanceModificationData id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentInstanceModificationData" ALTER COLUMN id SET DEFAULT nextval('public."DocumentInstanceModificationData_id_seq"'::regclass);


--
-- Name: DocumentProcessResult id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentProcessResult" ALTER COLUMN id SET DEFAULT nextval('public."DocumentProcessResult_id_seq"'::regclass);


--
-- Name: DocumentText id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentText" ALTER COLUMN id SET DEFAULT nextval('public."DocumentText_id_seq"'::regclass);


--
-- Name: DocumentView id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentView" ALTER COLUMN id SET DEFAULT nextval('public."DocumentView_id_seq"'::regclass);


--
-- Name: EnterpriseEmailContacts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EnterpriseEmailContacts" ALTER COLUMN id SET DEFAULT nextval('public."EnterpriseEmailContacts_id_seq"'::regclass);


--
-- Name: EnterpriseIManageTenants id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EnterpriseIManageTenants" ALTER COLUMN id SET DEFAULT nextval('public."EnterpriseIManageTenants_id_seq"'::regclass);


--
-- Name: EnterpriseRules id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EnterpriseRules" ALTER COLUMN id SET DEFAULT nextval('public."EnterpriseRules_id_seq"'::regclass);


--
-- Name: Organization id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Organization" ALTER COLUMN id SET DEFAULT nextval('public."Organization_id_seq"'::regclass);


--
-- Name: OrganizationBilling id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationBilling" ALTER COLUMN id SET DEFAULT nextval('public."OrganizationBilling_id_seq"'::regclass);


--
-- Name: OrganizationDefaultSharePermission id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationDefaultSharePermission" ALTER COLUMN id SET DEFAULT nextval('public."OrganizationDefaultSharePermission_id_seq"'::regclass);


--
-- Name: OrganizationInvitation id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationInvitation" ALTER COLUMN id SET DEFAULT nextval('public."OrganizationInvitation_id_seq"'::regclass);


--
-- Name: OrganizationItJob id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationItJob" ALTER COLUMN id SET DEFAULT nextval('public."OrganizationItJob_id_seq"'::regclass);


--
-- Name: OrganizationRetentionPolicy id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationRetentionPolicy" ALTER COLUMN id SET DEFAULT nextval('public."OrganizationRetentionPolicy_id_seq"'::regclass);


--
-- Name: PdfHighlightRect id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfHighlightRect" ALTER COLUMN id SET DEFAULT nextval('public."PdfHighlightRect_id_seq"'::regclass);


--
-- Name: Thread id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Thread" ALTER COLUMN id SET DEFAULT nextval('public."Thread_id_seq"'::regclass);


--
-- Name: UploadJob id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UploadJob" ALTER COLUMN id SET DEFAULT nextval('public."UploadJob_id_seq"'::regclass);


--
-- Name: contacts_backfill_outbox id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts_backfill_outbox ALTER COLUMN id SET DEFAULT nextval('public.contacts_backfill_outbox_id_seq'::regclass);


--
-- Name: contacts_connections id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts_connections ALTER COLUMN id SET DEFAULT nextval('public.contacts_connections_id_seq'::regclass);


--
-- Name: entity_access id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_access ALTER COLUMN id SET DEFAULT nextval('public.entity_access_id_seq'::regclass);


--
-- Name: frecency_aggregates id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.frecency_aggregates ALTER COLUMN id SET DEFAULT nextval('public.frecency_aggregates_id_seq'::regclass);


--
-- Name: frecency_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.frecency_events ALTER COLUMN id SET DEFAULT nextval('public.frecency_events_id_seq'::regclass);


--
-- Name: Artifact Artifact_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Artifact"
    ADD CONSTRAINT "Artifact_pkey" PRIMARY KEY ("messageId", digest);


--
-- Name: BlockedEmail BlockedEmail_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."BlockedEmail"
    ADD CONSTRAINT "BlockedEmail_pkey" PRIMARY KEY (email);


--
-- Name: BomPart BomPart_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."BomPart"
    ADD CONSTRAINT "BomPart_pkey" PRIMARY KEY (id);


--
-- Name: ChannelSharePermission ChannelSharePermission_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ChannelSharePermission"
    ADD CONSTRAINT "ChannelSharePermission_pkey" PRIMARY KEY (channel_id, share_permission_id);


--
-- Name: ChatAttachment ChatAttachment_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ChatAttachment"
    ADD CONSTRAINT "ChatAttachment_pkey" PRIMARY KEY (id);


--
-- Name: ChatMessage ChatMessage_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ChatMessage"
    ADD CONSTRAINT "ChatMessage_pkey" PRIMARY KEY (id);


--
-- Name: ChatPermission ChatPermission_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ChatPermission"
    ADD CONSTRAINT "ChatPermission_pkey" PRIMARY KEY ("chatId");


--
-- Name: Chat Chat_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Chat"
    ADD CONSTRAINT "Chat_pkey" PRIMARY KEY (id);


--
-- Name: Comment Comment_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Comment"
    ADD CONSTRAINT "Comment_pkey" PRIMARY KEY (id);


--
-- Name: DocumentBom DocumentBom_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentBom"
    ADD CONSTRAINT "DocumentBom_pkey" PRIMARY KEY (id);


--
-- Name: DocumentFamily DocumentFamily_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentFamily"
    ADD CONSTRAINT "DocumentFamily_pkey" PRIMARY KEY (id);


--
-- Name: DocumentInstanceModificationData DocumentInstanceModificationData_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentInstanceModificationData"
    ADD CONSTRAINT "DocumentInstanceModificationData_pkey" PRIMARY KEY (id);


--
-- Name: DocumentInstance DocumentInstance_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentInstance"
    ADD CONSTRAINT "DocumentInstance_pkey" PRIMARY KEY (id);


--
-- Name: DocumentPermission DocumentPermission_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentPermission"
    ADD CONSTRAINT "DocumentPermission_pkey" PRIMARY KEY ("documentId");


--
-- Name: DocumentProcessResult DocumentProcessResult_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentProcessResult"
    ADD CONSTRAINT "DocumentProcessResult_pkey" PRIMARY KEY (id);


--
-- Name: DocumentSummary DocumentSummary_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentSummary"
    ADD CONSTRAINT "DocumentSummary_pkey" PRIMARY KEY (id);


--
-- Name: DocumentTextParts DocumentTextParts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentTextParts"
    ADD CONSTRAINT "DocumentTextParts_pkey" PRIMARY KEY (id);


--
-- Name: DocumentText DocumentText_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentText"
    ADD CONSTRAINT "DocumentText_pkey" PRIMARY KEY (id);


--
-- Name: DocumentView DocumentView_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentView"
    ADD CONSTRAINT "DocumentView_pkey" PRIMARY KEY (id);


--
-- Name: Document Document_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Document"
    ADD CONSTRAINT "Document_pkey" PRIMARY KEY (id);


--
-- Name: EmailInsightsBackfillBatch EmailInsightsBackfillBatch_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EmailInsightsBackfillBatch"
    ADD CONSTRAINT "EmailInsightsBackfillBatch_pkey" PRIMARY KEY (id);


--
-- Name: EmailInsightsBackfillJob EmailInsightsBackfillJob_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EmailInsightsBackfillJob"
    ADD CONSTRAINT "EmailInsightsBackfillJob_pkey" PRIMARY KEY (id);


--
-- Name: EmailThreadPermission EmailThreadPermission_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EmailThreadPermission"
    ADD CONSTRAINT "EmailThreadPermission_pkey" PRIMARY KEY ("threadId");


--
-- Name: EnterpriseEmailContacts EnterpriseEmailContacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EnterpriseEmailContacts"
    ADD CONSTRAINT "EnterpriseEmailContacts_pkey" PRIMARY KEY (id);


--
-- Name: EnterpriseIManageTenants EnterpriseIManageTenants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EnterpriseIManageTenants"
    ADD CONSTRAINT "EnterpriseIManageTenants_pkey" PRIMARY KEY (id);


--
-- Name: EnterpriseRules EnterpriseRules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EnterpriseRules"
    ADD CONSTRAINT "EnterpriseRules_pkey" PRIMARY KEY (id);


--
-- Name: ExperimentLog ExperimentLog_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ExperimentLog"
    ADD CONSTRAINT "ExperimentLog_pkey" PRIMARY KEY (user_id, experiment_id);


--
-- Name: Experiment Experiment_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Experiment"
    ADD CONSTRAINT "Experiment_pkey" PRIMARY KEY (id);


--
-- Name: HearthPromptAttachment HearthPromptAttachment_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."HearthPromptAttachment"
    ADD CONSTRAINT "HearthPromptAttachment_pkey" PRIMARY KEY (id);


--
-- Name: HearthPromptPermission HearthPromptPermission_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."HearthPromptPermission"
    ADD CONSTRAINT "HearthPromptPermission_pkey" PRIMARY KEY (hearth_prompt_id);


--
-- Name: HearthPrompt HearthPrompt_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."HearthPrompt"
    ADD CONSTRAINT "HearthPrompt_pkey" PRIMARY KEY (id);


--
-- Name: InsightContext InsightContext_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."InsightContext"
    ADD CONSTRAINT "InsightContext_pkey" PRIMARY KEY (id);


--
-- Name: InstructionsDocuments InstructionsDocuments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."InstructionsDocuments"
    ADD CONSTRAINT "InstructionsDocuments_pkey" PRIMARY KEY ("documentId");


--
-- Name: ItemLastAccessed ItemLastAccessed_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ItemLastAccessed"
    ADD CONSTRAINT "ItemLastAccessed_pkey" PRIMARY KEY (item_id, item_type);


--
-- Name: JobToDocumentProcessResult JobToDocumentProcessResult_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."JobToDocumentProcessResult"
    ADD CONSTRAINT "JobToDocumentProcessResult_pkey" PRIMARY KEY ("jobId", "documentProcessResultId");


--
-- Name: OrganizationBilling OrganizationBilling_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationBilling"
    ADD CONSTRAINT "OrganizationBilling_pkey" PRIMARY KEY (id);


--
-- Name: OrganizationDefaultSharePermission OrganizationDefaultSharePermission_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationDefaultSharePermission"
    ADD CONSTRAINT "OrganizationDefaultSharePermission_pkey" PRIMARY KEY (id);


--
-- Name: OrganizationEmailMatches OrganizationEmailMatches_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationEmailMatches"
    ADD CONSTRAINT "OrganizationEmailMatches_pkey" PRIMARY KEY (email);


--
-- Name: OrganizationInvitation OrganizationInvitation_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationInvitation"
    ADD CONSTRAINT "OrganizationInvitation_pkey" PRIMARY KEY (id);


--
-- Name: OrganizationItJob OrganizationItJob_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationItJob"
    ADD CONSTRAINT "OrganizationItJob_pkey" PRIMARY KEY (id);


--
-- Name: OrganizationRetentionPolicy OrganizationRetentionPolicy_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationRetentionPolicy"
    ADD CONSTRAINT "OrganizationRetentionPolicy_pkey" PRIMARY KEY (id);


--
-- Name: Organization Organization_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Organization"
    ADD CONSTRAINT "Organization_pkey" PRIMARY KEY (id);


--
-- Name: PdfHighlightAnchor PdfHighlightAnchor_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfHighlightAnchor"
    ADD CONSTRAINT "PdfHighlightAnchor_pkey" PRIMARY KEY (uuid);


--
-- Name: PdfHighlightRect PdfHighlightRect_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfHighlightRect"
    ADD CONSTRAINT "PdfHighlightRect_pkey" PRIMARY KEY (id);


--
-- Name: PdfPlaceableCommentAnchor PdfPlaceableCommentAnchor_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfPlaceableCommentAnchor"
    ADD CONSTRAINT "PdfPlaceableCommentAnchor_pkey" PRIMARY KEY (uuid);


--
-- Name: Permission Permission_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Permission"
    ADD CONSTRAINT "Permission_pkey" PRIMARY KEY (id);


--
-- Name: Pin Pin_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Pin"
    ADD CONSTRAINT "Pin_pkey" PRIMARY KEY ("userId", "pinnedItemId", "pinnedItemType");


--
-- Name: ProjectPermission ProjectPermission_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ProjectPermission"
    ADD CONSTRAINT "ProjectPermission_pkey" PRIMARY KEY ("projectId");


--
-- Name: Project Project_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Project"
    ADD CONSTRAINT "Project_pkey" PRIMARY KEY (id);


--
-- Name: Role Role_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Role"
    ADD CONSTRAINT "Role_pkey" PRIMARY KEY (id);


--
-- Name: RolesOnOrganizations RolesOnOrganizations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."RolesOnOrganizations"
    ADD CONSTRAINT "RolesOnOrganizations_pkey" PRIMARY KEY ("organizationId", "roleId");


--
-- Name: RolesOnPermissions RolesOnPermissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."RolesOnPermissions"
    ADD CONSTRAINT "RolesOnPermissions_pkey" PRIMARY KEY ("permissionId", "roleId");


--
-- Name: RolesOnUsers RolesOnUsers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."RolesOnUsers"
    ADD CONSTRAINT "RolesOnUsers_pkey" PRIMARY KEY ("userId", "roleId");


--
-- Name: SharePermission SharePermission_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."SharePermission"
    ADD CONSTRAINT "SharePermission_pkey" PRIMARY KEY (id);


--
-- Name: Thread Thread_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Thread"
    ADD CONSTRAINT "Thread_pkey" PRIMARY KEY (id);


--
-- Name: UploadJob UploadJob_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UploadJob"
    ADD CONSTRAINT "UploadJob_pkey" PRIMARY KEY (id);


--
-- Name: UserApiKey UserApiKey_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserApiKey"
    ADD CONSTRAINT "UserApiKey_pkey" PRIMARY KEY (user_id, key);


--
-- Name: UserDocumentViewLocation UserDocumentViewLocation_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserDocumentViewLocation"
    ADD CONSTRAINT "UserDocumentViewLocation_pkey" PRIMARY KEY (user_id, document_id);


--
-- Name: UserHistory UserHistory_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserHistory"
    ADD CONSTRAINT "UserHistory_pkey" PRIMARY KEY ("userId", "itemId", "itemType");


--
-- Name: UserInsightBatch UserInsightBatch_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserInsightBatch"
    ADD CONSTRAINT "UserInsightBatch_pkey" PRIMARY KEY (id);


--
-- Name: UserInsights UserInsights_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserInsights"
    ADD CONSTRAINT "UserInsights_pkey" PRIMARY KEY (id);


--
-- Name: User User_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."User"
    ADD CONSTRAINT "User_pkey" PRIMARY KEY (id);


--
-- Name: WebAnnotations WebAnnotations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."WebAnnotations"
    ADD CONSTRAINT "WebAnnotations_pkey" PRIMARY KEY (id);


--
-- Name: WebsocketConnectionPermissions WebsocketConnectionPermissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."WebsocketConnectionPermissions"
    ADD CONSTRAINT "WebsocketConnectionPermissions_pkey" PRIMARY KEY ("connectionId");


--
-- Name: _sqlx_migrations _sqlx_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public._sqlx_migrations
    ADD CONSTRAINT _sqlx_migrations_pkey PRIMARY KEY (version);


--
-- Name: account_merge_request account_merge_request_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_merge_request
    ADD CONSTRAINT account_merge_request_pkey PRIMARY KEY (id);


--
-- Name: action_execution_record action_execution_record_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.action_execution_record
    ADD CONSTRAINT action_execution_record_pkey PRIMARY KEY (id);


--
-- Name: active_streams active_streams_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.active_streams
    ADD CONSTRAINT active_streams_pkey PRIMARY KEY (entity_id, stream_key);


--
-- Name: ai_pricing ai_pricing_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ai_pricing
    ADD CONSTRAINT ai_pricing_pkey PRIMARY KEY (model);


--
-- Name: ai_projection ai_projection_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ai_projection
    ADD CONSTRAINT ai_projection_pkey PRIMARY KEY (id);


--
-- Name: ai_usage ai_usage_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ai_usage
    ADD CONSTRAINT ai_usage_pkey PRIMARY KEY (id);


--
-- Name: bot_tokens bot_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_tokens
    ADD CONSTRAINT bot_tokens_pkey PRIMARY KEY (id);


--
-- Name: bots bots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_pkey PRIMARY KEY (id);


--
-- Name: call_participants call_participants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_participants
    ADD CONSTRAINT call_participants_pkey PRIMARY KEY (call_id, user_id);


--
-- Name: call_record_participants call_record_participants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_record_participants
    ADD CONSTRAINT call_record_participants_pkey PRIMARY KEY (call_record_id, user_id);


--
-- Name: call_record_transcripts call_record_transcripts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_record_transcripts
    ADD CONSTRAINT call_record_transcripts_pkey PRIMARY KEY (id);


--
-- Name: call_records call_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_records
    ADD CONSTRAINT call_records_pkey PRIMARY KEY (id);


--
-- Name: call_transcripts call_transcripts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_transcripts
    ADD CONSTRAINT call_transcripts_pkey PRIMARY KEY (id);


--
-- Name: call_transcripts call_transcripts_segment_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_transcripts
    ADD CONSTRAINT call_transcripts_segment_unique UNIQUE (call_id, segment_id);


--
-- Name: calls calls_one_per_channel; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.calls
    ADD CONSTRAINT calls_one_per_channel UNIQUE (channel_id);


--
-- Name: calls calls_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.calls
    ADD CONSTRAINT calls_pkey PRIMARY KEY (id);


--
-- Name: channel_notification_email_sent channel_notification_email_sent_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_notification_email_sent
    ADD CONSTRAINT channel_notification_email_sent_pkey PRIMARY KEY (channel_id, user_id);


--
-- Name: comms_activity comms_activity_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_activity
    ADD CONSTRAINT comms_activity_pkey PRIMARY KEY (id);


--
-- Name: comms_attachments comms_attachments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_attachments
    ADD CONSTRAINT comms_attachments_pkey PRIMARY KEY (id);


--
-- Name: comms_channel_participants comms_channel_participants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_channel_participants
    ADD CONSTRAINT comms_channel_participants_pkey PRIMARY KEY (channel_id, user_id);


--
-- Name: comms_channels comms_channels_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_channels
    ADD CONSTRAINT comms_channels_pkey PRIMARY KEY (id);


--
-- Name: comms_entity_mentions comms_entity_mentions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_entity_mentions
    ADD CONSTRAINT comms_entity_mentions_pkey PRIMARY KEY (id);


--
-- Name: comms_messages comms_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_messages
    ADD CONSTRAINT comms_messages_pkey PRIMARY KEY (id);


--
-- Name: comms_reactions comms_reactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_reactions
    ADD CONSTRAINT comms_reactions_pkey PRIMARY KEY (message_id, emoji, user_id);


--
-- Name: comms_activity comms_unique_user_channel; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_activity
    ADD CONSTRAINT comms_unique_user_channel UNIQUE (user_id, channel_id);


--
-- Name: contacts_backfill_outbox contacts_backfill_outbox_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts_backfill_outbox
    ADD CONSTRAINT contacts_backfill_outbox_pkey PRIMARY KEY (id);


--
-- Name: contacts_connections contacts_connections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts_connections
    ADD CONSTRAINT contacts_connections_pkey PRIMARY KEY (id);


--
-- Name: contacts_connections contacts_connections_user1_user2_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts_connections
    ADD CONSTRAINT contacts_connections_user1_user2_key UNIQUE (user1, user2);


--
-- Name: crm_cleanup_candidates crm_cleanup_candidates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_cleanup_candidates
    ADD CONSTRAINT crm_cleanup_candidates_pkey PRIMARY KEY (id);


--
-- Name: crm_cleanup_jobs crm_cleanup_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_cleanup_jobs
    ADD CONSTRAINT crm_cleanup_jobs_pkey PRIMARY KEY (id);


--
-- Name: crm_comment crm_comment_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_comment
    ADD CONSTRAINT crm_comment_pkey PRIMARY KEY (id);


--
-- Name: crm_companies crm_companies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_companies
    ADD CONSTRAINT crm_companies_pkey PRIMARY KEY (id);


--
-- Name: crm_contact_sources crm_contact_sources_contact_id_link_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_contact_sources
    ADD CONSTRAINT crm_contact_sources_contact_id_link_id_key UNIQUE (contact_id, link_id);


--
-- Name: crm_contact_sources crm_contact_sources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_contact_sources
    ADD CONSTRAINT crm_contact_sources_pkey PRIMARY KEY (id);


--
-- Name: crm_contacts crm_contacts_company_id_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_contacts
    ADD CONSTRAINT crm_contacts_company_id_email_key UNIQUE (company_id, email);


--
-- Name: crm_contacts crm_contacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_contacts
    ADD CONSTRAINT crm_contacts_pkey PRIMARY KEY (id);


--
-- Name: crm_domain_directory crm_domain_directory_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_domain_directory
    ADD CONSTRAINT crm_domain_directory_pkey PRIMARY KEY (id);


--
-- Name: crm_domains crm_domains_company_id_domain_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_domains
    ADD CONSTRAINT crm_domains_company_id_domain_key UNIQUE (company_id, domain);


--
-- Name: crm_domains crm_domains_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_domains
    ADD CONSTRAINT crm_domains_pkey PRIMARY KEY (id);


--
-- Name: crm_thread crm_thread_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_thread
    ADD CONSTRAINT crm_thread_pkey PRIMARY KEY (id);


--
-- Name: document_email document_email_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.document_email
    ADD CONSTRAINT document_email_pkey PRIMARY KEY (document_id, email_attachment_id);


--
-- Name: document_sub_type document_sub_type_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.document_sub_type
    ADD CONSTRAINT document_sub_type_pkey PRIMARY KEY (document_id);


--
-- Name: email_attachments_drafts email_attachments_drafts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments_drafts
    ADD CONSTRAINT email_attachments_drafts_pkey PRIMARY KEY (id);


--
-- Name: email_attachments_drafts email_attachments_drafts_s3_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments_drafts
    ADD CONSTRAINT email_attachments_drafts_s3_key_key UNIQUE (s3_key);


--
-- Name: email_attachments_fwd email_attachments_fwd_message_id_attachment_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments_fwd
    ADD CONSTRAINT email_attachments_fwd_message_id_attachment_id_key UNIQUE (message_id, attachment_id);


--
-- Name: email_attachments email_attachments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments
    ADD CONSTRAINT email_attachments_pkey PRIMARY KEY (id);


--
-- Name: email_attachments_sfs email_attachments_sfs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments_sfs
    ADD CONSTRAINT email_attachments_sfs_pkey PRIMARY KEY (id);


--
-- Name: email_backfill_jobs email_backfill_job_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_backfill_jobs
    ADD CONSTRAINT email_backfill_job_pkey PRIMARY KEY (id);


--
-- Name: email_contacts email_contacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_contacts
    ADD CONSTRAINT email_contacts_pkey PRIMARY KEY (id);


--
-- Name: email_filters email_filters_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_filters
    ADD CONSTRAINT email_filters_pkey PRIMARY KEY (id);


--
-- Name: email_gmail_histories email_gmail_histories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_gmail_histories
    ADD CONSTRAINT email_gmail_histories_pkey PRIMARY KEY (link_id);


--
-- Name: email_labels email_labels_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_labels
    ADD CONSTRAINT email_labels_pkey PRIMARY KEY (id);


--
-- Name: email_links_history email_links_history_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_links_history
    ADD CONSTRAINT email_links_history_pkey PRIMARY KEY (id);


--
-- Name: email_links email_links_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_links
    ADD CONSTRAINT email_links_pkey PRIMARY KEY (id);


--
-- Name: email_message_labels email_message_labels_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_message_labels
    ADD CONSTRAINT email_message_labels_pkey PRIMARY KEY (message_id, label_id);


--
-- Name: email_message_recipients email_message_recipients_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_message_recipients
    ADD CONSTRAINT email_message_recipients_pkey PRIMARY KEY (message_id, contact_id, recipient_type);


--
-- Name: email_messages email_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_messages
    ADD CONSTRAINT email_messages_pkey PRIMARY KEY (id);


--
-- Name: email_scheduled_messages email_scheduled_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_scheduled_messages
    ADD CONSTRAINT email_scheduled_messages_pkey PRIMARY KEY (link_id, message_id);


--
-- Name: email_settings email_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_settings
    ADD CONSTRAINT email_settings_pkey PRIMARY KEY (link_id);


--
-- Name: email_sfs_mappings email_sfs_mappings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_sfs_mappings
    ADD CONSTRAINT email_sfs_mappings_pkey PRIMARY KEY (source);


--
-- Name: email_sync_tokens email_sync_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_sync_tokens
    ADD CONSTRAINT email_sync_tokens_pkey PRIMARY KEY (link_id);


--
-- Name: email_threads email_threads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_threads
    ADD CONSTRAINT email_threads_pkey PRIMARY KEY (id);


--
-- Name: email_attachments email_uq_attachments_message_id_provider_attachment_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments
    ADD CONSTRAINT email_uq_attachments_message_id_provider_attachment_id UNIQUE (message_id, provider_attachment_id);


--
-- Name: email_labels email_uq_labels_link_id_provider_label_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_labels
    ADD CONSTRAINT email_uq_labels_link_id_provider_label_id UNIQUE (link_id, provider_label_id);


--
-- Name: email_links email_uq_links_user_email_provider; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_links
    ADD CONSTRAINT email_uq_links_user_email_provider UNIQUE (fusionauth_user_id, email_address, provider);


--
-- Name: email_user_history email_user_history_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_user_history
    ADD CONSTRAINT email_user_history_pkey PRIMARY KEY (link_id, thread_id);


--
-- Name: entity_access entity_access_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_access
    ADD CONSTRAINT entity_access_pkey PRIMARY KEY (id);


--
-- Name: entity_properties entity_properties_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_properties
    ADD CONSTRAINT entity_properties_pkey PRIMARY KEY (id);


--
-- Name: excluded_default_view excluded_default_view_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.excluded_default_view
    ADD CONSTRAINT excluded_default_view_pkey PRIMARY KEY (id);


--
-- Name: favorite favorite_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.favorite
    ADD CONSTRAINT favorite_pkey PRIMARY KEY (user_id, entity_type, entity_id);


--
-- Name: foreign_entity foreign_entity_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.foreign_entity
    ADD CONSTRAINT foreign_entity_pkey PRIMARY KEY (id);


--
-- Name: frecency_aggregates frecency_aggregates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.frecency_aggregates
    ADD CONSTRAINT frecency_aggregates_pkey PRIMARY KEY (id);


--
-- Name: frecency_events frecency_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.frecency_events
    ADD CONSTRAINT frecency_events_pkey PRIMARY KEY (id);


--
-- Name: github_app_installation_installer github_app_installation_installer_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_installation_installer
    ADD CONSTRAINT github_app_installation_installer_pkey PRIMARY KEY (installation_id);


--
-- Name: github_app_installation github_app_installation_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_installation
    ADD CONSTRAINT github_app_installation_pkey PRIMARY KEY (id, source_id, source_type);


--
-- Name: github_links github_links_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_links
    ADD CONSTRAINT github_links_pkey PRIMARY KEY (id);


--
-- Name: github_pr_tasks github_pr_tasks_github_key_task_id_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_pr_tasks
    ADD CONSTRAINT github_pr_tasks_github_key_task_id_unique UNIQUE (github_key, task_id);


--
-- Name: github_pr_tasks github_pr_tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_pr_tasks
    ADD CONSTRAINT github_pr_tasks_pkey PRIMARY KEY (id);


--
-- Name: hearth_user_email_verification hearth_user_email_verification_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_email_verification
    ADD CONSTRAINT hearth_user_email_verification_pkey PRIMARY KEY (hearth_user_id, email);


--
-- Name: hearth_user_info hearth_user_info_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_info
    ADD CONSTRAINT hearth_user_info_pkey PRIMARY KEY (hearth_user_id);


--
-- Name: hearth_user_links hearth_user_links_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_links
    ADD CONSTRAINT hearth_user_links_pkey PRIMARY KEY (primary_hearth_id, child_hearth_id, link_id);


--
-- Name: hearth_user hearth_user_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user
    ADD CONSTRAINT hearth_user_pkey PRIMARY KEY (id);


--
-- Name: hearth_user_voice hearth_user_voice_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_voice
    ADD CONSTRAINT hearth_user_voice_pkey PRIMARY KEY (hearth_user_id, voice_id);


--
-- Name: id_mapping id_mapping_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.id_mapping
    ADD CONSTRAINT id_mapping_pkey PRIMARY KEY (source_id);


--
-- Name: import_entity import_entity_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.import_entity
    ADD CONSTRAINT import_entity_pkey PRIMARY KEY (id);


--
-- Name: import_entity import_entity_user_id_source_foreign_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.import_entity
    ADD CONSTRAINT import_entity_user_id_source_foreign_id_key UNIQUE (user_id, source, foreign_id);


--
-- Name: import_run import_run_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.import_run
    ADD CONSTRAINT import_run_pkey PRIMARY KEY (user_id, source);


--
-- Name: in_progress_email_link in_progress_email_link_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.in_progress_email_link
    ADD CONSTRAINT in_progress_email_link_pkey PRIMARY KEY (id);


--
-- Name: in_progress_user_link in_progress_user_link_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.in_progress_user_link
    ADD CONSTRAINT in_progress_user_link_pkey PRIMARY KEY (id);


--
-- Name: mcp_servers mcp_servers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_servers
    ADD CONSTRAINT mcp_servers_pkey PRIMARY KEY (user_id, url);


--
-- Name: memory memory_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory
    ADD CONSTRAINT memory_pkey PRIMARY KEY (id);


--
-- Name: memory memory_user_id_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory
    ADD CONSTRAINT memory_user_id_unique UNIQUE (user_id);


--
-- Name: mobile_welcome_email mobile_welcome_email_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mobile_welcome_email
    ADD CONSTRAINT mobile_welcome_email_pkey PRIMARY KEY (email);


--
-- Name: notification_email_sent notification_email_sent_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_email_sent
    ADD CONSTRAINT notification_email_sent_pkey PRIMARY KEY (user_id);


--
-- Name: notification_email_unsubscribe_code notification_email_unsubscribe_code_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_email_unsubscribe_code
    ADD CONSTRAINT notification_email_unsubscribe_code_code_key UNIQUE (code);


--
-- Name: notification_email_unsubscribe_code notification_email_unsubscribe_code_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_email_unsubscribe_code
    ADD CONSTRAINT notification_email_unsubscribe_code_pkey PRIMARY KEY (email);


--
-- Name: notification_email_unsubscribe notification_email_unsubscribe_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_email_unsubscribe
    ADD CONSTRAINT notification_email_unsubscribe_pkey PRIMARY KEY (email);


--
-- Name: notification_message_receipt notification_message_receipt_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_message_receipt
    ADD CONSTRAINT notification_message_receipt_pkey PRIMARY KEY (message_id);


--
-- Name: notification notification_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification
    ADD CONSTRAINT notification_pkey PRIMARY KEY (id);


--
-- Name: notification_user_device_registration notification_user_device_registration_device_endpoint_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_user_device_registration
    ADD CONSTRAINT notification_user_device_registration_device_endpoint_key UNIQUE (device_endpoint);


--
-- Name: notification_user_device_registration notification_user_device_registration_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_user_device_registration
    ADD CONSTRAINT notification_user_device_registration_pkey PRIMARY KEY (id);


--
-- Name: processing_ai_projections processing_ai_projections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.processing_ai_projections
    ADD CONSTRAINT processing_ai_projections_pkey PRIMARY KEY (ai_projection_id, target_id);


--
-- Name: promoted_shared_mailboxes promoted_shared_mailboxes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promoted_shared_mailboxes
    ADD CONSTRAINT promoted_shared_mailboxes_pkey PRIMARY KEY (hearth_id);


--
-- Name: property_definitions property_definitions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.property_definitions
    ADD CONSTRAINT property_definitions_pkey PRIMARY KEY (id);


--
-- Name: property_options property_options_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.property_options
    ADD CONSTRAINT property_options_pkey PRIMARY KEY (id);


--
-- Name: referral_tracking referral_tracking_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.referral_tracking
    ADD CONSTRAINT referral_tracking_pkey PRIMARY KEY (id);


--
-- Name: resolved_message_content resolved_message_content_messageId_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolved_message_content
    ADD CONSTRAINT "resolved_message_content_messageId_key" UNIQUE ("messageId");


--
-- Name: resolved_message_content resolved_message_content_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolved_message_content
    ADD CONSTRAINT resolved_message_content_pkey PRIMARY KEY (id);


--
-- Name: saved_view saved_view_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saved_view
    ADD CONSTRAINT saved_view_pkey PRIMARY KEY (id);


--
-- Name: scheduled_action scheduled_action_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scheduled_action
    ADD CONSTRAINT scheduled_action_pkey PRIMARY KEY (id);


--
-- Name: task_duplicate_embedding task_duplicate_embedding_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_duplicate_embedding
    ADD CONSTRAINT task_duplicate_embedding_pkey PRIMARY KEY (document_id, search_key);


--
-- Name: task_duplicate_match task_duplicate_match_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_duplicate_match
    ADD CONSTRAINT task_duplicate_match_pkey PRIMARY KEY (id);


--
-- Name: team_crm_settings team_crm_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_crm_settings
    ADD CONSTRAINT team_crm_settings_pkey PRIMARY KEY (team_id);


--
-- Name: team_invite team_invite_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_invite
    ADD CONSTRAINT team_invite_pkey PRIMARY KEY (id);


--
-- Name: team team_owner_id_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team
    ADD CONSTRAINT team_owner_id_unique UNIQUE (owner_id);


--
-- Name: team team_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team
    ADD CONSTRAINT team_pkey PRIMARY KEY (id);


--
-- Name: team_task_counter team_task_counter_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_task_counter
    ADD CONSTRAINT team_task_counter_pkey PRIMARY KEY (team_id);


--
-- Name: team_task team_task_document_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_task
    ADD CONSTRAINT team_task_document_unique UNIQUE (document_id);


--
-- Name: team_task team_task_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_task
    ADD CONSTRAINT team_task_pkey PRIMARY KEY (team_id, task_num);


--
-- Name: team_user team_user_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_user
    ADD CONSTRAINT team_user_pkey PRIMARY KEY (user_id, team_id);


--
-- Name: team_user team_user_user_id_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_user
    ADD CONSTRAINT team_user_user_id_unique UNIQUE (user_id);


--
-- Name: entity_properties unique_entity_properties_assignment; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_properties
    ADD CONSTRAINT unique_entity_properties_assignment UNIQUE (entity_id, entity_type, property_definition_id);


--
-- Name: property_options unique_property_options_number_value; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.property_options
    ADD CONSTRAINT unique_property_options_number_value UNIQUE (property_definition_id, number_value);


--
-- Name: frecency_aggregates unique_user_entity; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.frecency_aggregates
    ADD CONSTRAINT unique_user_entity UNIQUE (user_id, entity_type, entity_id);


--
-- Name: user_ai_projection user_ai_projection_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_ai_projection
    ADD CONSTRAINT user_ai_projection_pkey PRIMARY KEY (target_id, ai_projection_id);


--
-- Name: user_mute_notification user_mute_notification_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_mute_notification
    ADD CONSTRAINT user_mute_notification_pkey PRIMARY KEY (user_id);


--
-- Name: user_notification_item_unsubscribe user_notification_item_unsubscribe_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_notification_item_unsubscribe
    ADD CONSTRAINT user_notification_item_unsubscribe_pkey PRIMARY KEY (user_id, item_id);


--
-- Name: user_notification user_notification_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_notification
    ADD CONSTRAINT user_notification_pkey PRIMARY KEY (user_id, notification_id);


--
-- Name: user_notification_type_preference user_notification_type_preference_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_notification_type_preference
    ADD CONSTRAINT user_notification_type_preference_pkey PRIMARY KEY (user_id, notification_event_type);


--
-- Name: user_onboarding user_onboarding_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_onboarding
    ADD CONSTRAINT user_onboarding_pkey PRIMARY KEY (user_id);


--
-- Name: voice voice_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.voice
    ADD CONSTRAINT voice_pkey PRIMARY KEY (id);


--
-- Name: webhook_delivery_attempt webhook_delivery_attempt_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_delivery_attempt
    ADD CONSTRAINT webhook_delivery_attempt_pkey PRIMARY KEY (id);


--
-- Name: webhook_delivery_attempt webhook_delivery_attempt_webhook_delivery_id_attempt_number_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_delivery_attempt
    ADD CONSTRAINT webhook_delivery_attempt_webhook_delivery_id_attempt_number_key UNIQUE (webhook_delivery_id, attempt_number);


--
-- Name: webhook_delivery webhook_delivery_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_delivery
    ADD CONSTRAINT webhook_delivery_pkey PRIMARY KEY (id);


--
-- Name: webhook_delivery webhook_delivery_webhook_id_event_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_delivery
    ADD CONSTRAINT webhook_delivery_webhook_id_event_id_key UNIQUE (webhook_id, event_id);


--
-- Name: webhook webhook_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook
    ADD CONSTRAINT webhook_pkey PRIMARY KEY (id);


--
-- Name: Artifact_digest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Artifact_digest_idx" ON public."Artifact" USING btree (digest);


--
-- Name: Artifact_documentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Artifact_documentId_idx" ON public."Artifact" USING btree ("documentId");


--
-- Name: Artifact_userId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Artifact_userId_idx" ON public."Artifact" USING btree ("userId");


--
-- Name: BomPart_documentBomId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "BomPart_documentBomId_idx" ON public."BomPart" USING btree ("documentBomId");


--
-- Name: BomPart_sha_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "BomPart_sha_idx" ON public."BomPart" USING btree (sha);


--
-- Name: ChannelSharePermission_channel_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "ChannelSharePermission_channel_id_idx" ON public."ChannelSharePermission" USING btree (channel_id);


--
-- Name: ChannelSharePermission_share_permission_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "ChannelSharePermission_share_permission_id_idx" ON public."ChannelSharePermission" USING btree (share_permission_id);


--
-- Name: ChatAttachment_chatId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "ChatAttachment_chatId_idx" ON public."ChatAttachment" USING btree ("chatId");


--
-- Name: ChatAttachment_entity_type_entity_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "ChatAttachment_entity_type_entity_id_idx" ON public."ChatAttachment" USING btree (entity_type, entity_id);


--
-- Name: ChatAttachment_messageId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "ChatAttachment_messageId_idx" ON public."ChatAttachment" USING btree ("messageId");


--
-- Name: ChatMessage_chatId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "ChatMessage_chatId_idx" ON public."ChatMessage" USING btree ("chatId");


--
-- Name: ChatPermission_chatId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "ChatPermission_chatId_key" ON public."ChatPermission" USING btree ("chatId");


--
-- Name: ChatPermission_sharePermissionId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "ChatPermission_sharePermissionId_key" ON public."ChatPermission" USING btree ("sharePermissionId");


--
-- Name: Chat_projectId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Chat_projectId_idx" ON public."Chat" USING btree ("projectId");


--
-- Name: Chat_userId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Chat_userId_idx" ON public."Chat" USING btree ("userId");


--
-- Name: Comment_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Comment_owner_idx" ON public."Comment" USING btree (owner);


--
-- Name: Comment_threadId_createdAt_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Comment_threadId_createdAt_idx" ON public."Comment" USING btree ("threadId", "createdAt");


--
-- Name: Comment_threadId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Comment_threadId_idx" ON public."Comment" USING btree ("threadId");


--
-- Name: DocumentBom_documentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "DocumentBom_documentId_idx" ON public."DocumentBom" USING btree ("documentId");


--
-- Name: DocumentFamily_rootDocumentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "DocumentFamily_rootDocumentId_idx" ON public."DocumentFamily" USING btree ("rootDocumentId");


--
-- Name: DocumentInstanceModificationData_documentInstanceId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "DocumentInstanceModificationData_documentInstanceId_idx" ON public."DocumentInstanceModificationData" USING btree ("documentInstanceId");


--
-- Name: DocumentInstance_documentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "DocumentInstance_documentId_idx" ON public."DocumentInstance" USING btree ("documentId");


--
-- Name: DocumentPermission_documentId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "DocumentPermission_documentId_key" ON public."DocumentPermission" USING btree ("documentId");


--
-- Name: DocumentPermission_sharePermissionId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "DocumentPermission_sharePermissionId_key" ON public."DocumentPermission" USING btree ("sharePermissionId");


--
-- Name: DocumentProcessResult_documentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "DocumentProcessResult_documentId_idx" ON public."DocumentProcessResult" USING btree ("documentId");


--
-- Name: DocumentProcessResult_documentId_jobType_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "DocumentProcessResult_documentId_jobType_key" ON public."DocumentProcessResult" USING btree ("documentId", "jobType");


--
-- Name: DocumentSummary_document_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "DocumentSummary_document_id_idx" ON public."DocumentSummary" USING btree (document_id);


--
-- Name: DocumentTextParts_documentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "DocumentTextParts_documentId_idx" ON public."DocumentTextParts" USING btree ("documentId");


--
-- Name: DocumentText_documentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "DocumentText_documentId_idx" ON public."DocumentText" USING btree ("documentId");


--
-- Name: DocumentText_documentId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "DocumentText_documentId_key" ON public."DocumentText" USING btree ("documentId");


--
-- Name: DocumentView_document_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "DocumentView_document_id_idx" ON public."DocumentView" USING btree (document_id);


--
-- Name: DocumentView_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "DocumentView_user_id_idx" ON public."DocumentView" USING btree (user_id);


--
-- Name: Document_branchedFromId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Document_branchedFromId_idx" ON public."Document" USING btree ("branchedFromId");


--
-- Name: Document_documentFamilyId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Document_documentFamilyId_idx" ON public."Document" USING btree ("documentFamilyId");


--
-- Name: Document_owner_deletedAt_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Document_owner_deletedAt_idx" ON public."Document" USING btree (owner, "deletedAt");


--
-- Name: Document_owner_fileType_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Document_owner_fileType_idx" ON public."Document" USING btree (owner, "fileType");


--
-- Name: Document_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Document_owner_idx" ON public."Document" USING btree (owner);


--
-- Name: Document_projectId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Document_projectId_idx" ON public."Document" USING btree ("projectId");


--
-- Name: EmailThreadPermission_sharePermissionId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "EmailThreadPermission_sharePermissionId_key" ON public."EmailThreadPermission" USING btree ("sharePermissionId");


--
-- Name: EmailThreadPermission_threadId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "EmailThreadPermission_threadId_key" ON public."EmailThreadPermission" USING btree ("threadId");


--
-- Name: EnterpriseRules_organizationId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "EnterpriseRules_organizationId_key" ON public."EnterpriseRules" USING btree ("organizationId");


--
-- Name: ExperimentLog_experiment_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "ExperimentLog_experiment_id_idx" ON public."ExperimentLog" USING btree (experiment_id);


--
-- Name: ExperimentLog_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "ExperimentLog_user_id_idx" ON public."ExperimentLog" USING btree (user_id);


--
-- Name: Experiment_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "Experiment_id_key" ON public."Experiment" USING btree (id);


--
-- Name: HearthPromptAttachment_attachment_type_attachment_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "HearthPromptAttachment_attachment_type_attachment_id_idx" ON public."HearthPromptAttachment" USING btree (attachment_type, attachment_id);


--
-- Name: HearthPromptAttachment_hearth_prompt_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "HearthPromptAttachment_hearth_prompt_id_idx" ON public."HearthPromptAttachment" USING btree (hearth_prompt_id);


--
-- Name: HearthPromptPermission_hearth_prompt_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "HearthPromptPermission_hearth_prompt_id_key" ON public."HearthPromptPermission" USING btree (hearth_prompt_id);


--
-- Name: HearthPromptPermission_share_permission_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "HearthPromptPermission_share_permission_id_key" ON public."HearthPromptPermission" USING btree (share_permission_id);


--
-- Name: HearthPrompt_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "HearthPrompt_user_id_idx" ON public."HearthPrompt" USING btree (user_id);


--
-- Name: InstructionsDocuments_userId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "InstructionsDocuments_userId_idx" ON public."InstructionsDocuments" USING btree ("userId");


--
-- Name: InstructionsDocuments_userId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "InstructionsDocuments_userId_key" ON public."InstructionsDocuments" USING btree ("userId");


--
-- Name: ItemLastAccessed_item_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "ItemLastAccessed_item_id_idx" ON public."ItemLastAccessed" USING btree (item_id);


--
-- Name: ItemLastAccessed_item_type_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "ItemLastAccessed_item_type_idx" ON public."ItemLastAccessed" USING btree (item_type);


--
-- Name: JobToDocumentProcessResult_documentProcessResultId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "JobToDocumentProcessResult_documentProcessResultId_idx" ON public."JobToDocumentProcessResult" USING btree ("documentProcessResultId");


--
-- Name: JobToDocumentProcessResult_jobId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "JobToDocumentProcessResult_jobId_idx" ON public."JobToDocumentProcessResult" USING btree ("jobId");


--
-- Name: JobToDocumentProcessResult_jobId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "JobToDocumentProcessResult_jobId_key" ON public."JobToDocumentProcessResult" USING btree ("jobId");


--
-- Name: OrganizationBilling_email_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "OrganizationBilling_email_key" ON public."OrganizationBilling" USING btree (email);


--
-- Name: OrganizationDefaultSharePermission_organization_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "OrganizationDefaultSharePermission_organization_id_idx" ON public."OrganizationDefaultSharePermission" USING btree (organization_id);


--
-- Name: OrganizationDefaultSharePermission_organization_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "OrganizationDefaultSharePermission_organization_id_key" ON public."OrganizationDefaultSharePermission" USING btree (organization_id);


--
-- Name: OrganizationEmailMatches_organizationId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "OrganizationEmailMatches_organizationId_idx" ON public."OrganizationEmailMatches" USING btree ("organizationId");


--
-- Name: OrganizationIT_email_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "OrganizationIT_email_key" ON public."OrganizationIT" USING btree (email);


--
-- Name: OrganizationInvitation_email_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "OrganizationInvitation_email_idx" ON public."OrganizationInvitation" USING btree (email);


--
-- Name: OrganizationInvitation_organization_id_email_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "OrganizationInvitation_organization_id_email_key" ON public."OrganizationInvitation" USING btree (organization_id, email);


--
-- Name: OrganizationInvitation_organization_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "OrganizationInvitation_organization_id_idx" ON public."OrganizationInvitation" USING btree (organization_id);


--
-- Name: OrganizationItJob_organizationId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "OrganizationItJob_organizationId_key" ON public."OrganizationItJob" USING btree ("organizationId");


--
-- Name: OrganizationRetentionPolicy_organization_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "OrganizationRetentionPolicy_organization_id_idx" ON public."OrganizationRetentionPolicy" USING btree (organization_id);


--
-- Name: OrganizationRetentionPolicy_organization_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "OrganizationRetentionPolicy_organization_id_key" ON public."OrganizationRetentionPolicy" USING btree (organization_id);


--
-- Name: Organization_name_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "Organization_name_key" ON public."Organization" USING btree (name);


--
-- Name: Organization_stripeCustomerId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "Organization_stripeCustomerId_key" ON public."Organization" USING btree ("stripeCustomerId");


--
-- Name: PdfHighlightAnchor_documentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "PdfHighlightAnchor_documentId_idx" ON public."PdfHighlightAnchor" USING btree ("documentId");


--
-- Name: PdfHighlightAnchor_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "PdfHighlightAnchor_owner_idx" ON public."PdfHighlightAnchor" USING btree (owner);


--
-- Name: PdfHighlightAnchor_threadId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "PdfHighlightAnchor_threadId_idx" ON public."PdfHighlightAnchor" USING btree ("threadId");


--
-- Name: PdfHighlightRect_pdfHighlightAnchorId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "PdfHighlightRect_pdfHighlightAnchorId_idx" ON public."PdfHighlightRect" USING btree ("pdfHighlightAnchorId");


--
-- Name: PdfPlaceableCommentAnchor_documentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "PdfPlaceableCommentAnchor_documentId_idx" ON public."PdfPlaceableCommentAnchor" USING btree ("documentId");


--
-- Name: PdfPlaceableCommentAnchor_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "PdfPlaceableCommentAnchor_owner_idx" ON public."PdfPlaceableCommentAnchor" USING btree (owner);


--
-- Name: PdfPlaceableCommentAnchor_threadId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "PdfPlaceableCommentAnchor_threadId_idx" ON public."PdfPlaceableCommentAnchor" USING btree ("threadId");


--
-- Name: Pin_pinnedItemId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Pin_pinnedItemId_idx" ON public."Pin" USING btree ("pinnedItemId");


--
-- Name: Pin_userId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Pin_userId_idx" ON public."Pin" USING btree ("userId");


--
-- Name: ProjectPermission_projectId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "ProjectPermission_projectId_key" ON public."ProjectPermission" USING btree ("projectId");


--
-- Name: ProjectPermission_sharePermissionId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "ProjectPermission_sharePermissionId_key" ON public."ProjectPermission" USING btree ("sharePermissionId");


--
-- Name: Project_parentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Project_parentId_idx" ON public."Project" USING btree ("parentId");


--
-- Name: Project_uploadPending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Project_uploadPending_idx" ON public."Project" USING btree ("uploadPending");


--
-- Name: Project_userId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Project_userId_idx" ON public."Project" USING btree ("userId");


--
-- Name: ThreadAnchor_anchorId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "ThreadAnchor_anchorId_key" ON public."ThreadAnchor" USING btree ("anchorId");


--
-- Name: ThreadAnchor_threadId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "ThreadAnchor_threadId_key" ON public."ThreadAnchor" USING btree ("threadId");


--
-- Name: Thread_documentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Thread_documentId_idx" ON public."Thread" USING btree ("documentId");


--
-- Name: Thread_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "Thread_owner_idx" ON public."Thread" USING btree (owner);


--
-- Name: UploadJob_documentId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UploadJob_documentId_idx" ON public."UploadJob" USING btree ("documentId");


--
-- Name: UploadJob_jobId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UploadJob_jobId_idx" ON public."UploadJob" USING btree ("jobId");


--
-- Name: UserApiKey_key_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UserApiKey_key_idx" ON public."UserApiKey" USING btree (key);


--
-- Name: UserApiKey_key_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "UserApiKey_key_key" ON public."UserApiKey" USING btree (key);


--
-- Name: UserApiKey_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UserApiKey_user_id_idx" ON public."UserApiKey" USING btree (user_id);


--
-- Name: UserDocumentViewLocation_document_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UserDocumentViewLocation_document_id_idx" ON public."UserDocumentViewLocation" USING btree (document_id);


--
-- Name: UserDocumentViewLocation_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UserDocumentViewLocation_user_id_idx" ON public."UserDocumentViewLocation" USING btree (user_id);


--
-- Name: UserHistory_itemId_itemType_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UserHistory_itemId_itemType_idx" ON public."UserHistory" USING btree ("itemId", "itemType");


--
-- Name: UserHistory_userId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UserHistory_userId_idx" ON public."UserHistory" USING btree ("userId");


--
-- Name: UserHistory_userId_itemType_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UserHistory_userId_itemType_idx" ON public."UserHistory" USING btree ("userId", "itemType");


--
-- Name: UserInsightBatch_expiresAt_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UserInsightBatch_expiresAt_idx" ON public."UserInsightBatch" USING btree ("expiresAt");


--
-- Name: UserInsightBatch_userId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UserInsightBatch_userId_idx" ON public."UserInsightBatch" USING btree ("userId");


--
-- Name: UserInsightBatch_userId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "UserInsightBatch_userId_key" ON public."UserInsightBatch" USING btree ("userId");


--
-- Name: UserInsights_userId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "UserInsights_userId_idx" ON public."UserInsights" USING btree ("userId");


--
-- Name: User_email_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "User_email_idx" ON public."User" USING btree (email);


--
-- Name: User_email_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "User_email_key" ON public."User" USING btree (email);


--
-- Name: User_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "User_id_key" ON public."User" USING btree (id);


--
-- Name: User_organizationId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "User_organizationId_idx" ON public."User" USING btree ("organizationId");


--
-- Name: User_stripeCustomerId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "User_stripeCustomerId_key" ON public."User" USING btree ("stripeCustomerId");


--
-- Name: WebAnnotations_chatId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "WebAnnotations_chatId_idx" ON public."WebAnnotations" USING btree ("chatId");


--
-- Name: WebAnnotations_messageId_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "WebAnnotations_messageId_idx" ON public."WebAnnotations" USING btree ("messageId");


--
-- Name: WebsocketConnectionPermissions_connectionId_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX "WebsocketConnectionPermissions_connectionId_key" ON public."WebsocketConnectionPermissions" USING btree ("connectionId");


--
-- Name: account_merge_request_code_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX account_merge_request_code_idx ON public.account_merge_request USING btree (code);


--
-- Name: account_merge_request_hearth_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX account_merge_request_hearth_user_id_idx ON public.account_merge_request USING btree (hearth_user_id);


--
-- Name: account_merge_request_to_merge_hearth_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX account_merge_request_to_merge_hearth_user_id_idx ON public.account_merge_request USING btree (to_merge_hearth_user_id);


--
-- Name: account_merge_request_to_merge_hearth_user_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX account_merge_request_to_merge_hearth_user_id_key ON public.account_merge_request USING btree (to_merge_hearth_user_id);


--
-- Name: action_execution_record_action_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX action_execution_record_action_id_idx ON public.action_execution_record USING btree (action_id, start_time DESC);


--
-- Name: ai_usage_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ai_usage_created_at_idx ON public.ai_usage USING btree (created_at DESC);


--
-- Name: ai_usage_feature_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ai_usage_feature_idx ON public.ai_usage USING btree (feature);


--
-- Name: ai_usage_model_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ai_usage_model_idx ON public.ai_usage USING btree (model);


--
-- Name: ai_usage_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ai_usage_user_id_idx ON public.ai_usage USING btree (user_id);


--
-- Name: bot_tokens_bot_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bot_tokens_bot_id_idx ON public.bot_tokens USING btree (bot_id);


--
-- Name: bot_tokens_token_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX bot_tokens_token_idx ON public.bot_tokens USING btree (token);


--
-- Name: bots_owner_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bots_owner_user_id_idx ON public.bots USING btree (owner_user_id) WHERE (deleted_at IS NULL);


--
-- Name: bots_team_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bots_team_id_idx ON public.bots USING btree (team_id) WHERE (deleted_at IS NULL);


--
-- Name: call_participants_one_active_per_user; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX call_participants_one_active_per_user ON public.call_participants USING btree (user_id) WHERE (left_at IS NULL);


--
-- Name: comms_channels_join_code_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX comms_channels_join_code_uq ON public.comms_channels USING btree (join_code) WHERE (join_code IS NOT NULL);


--
-- Name: contacts_link_id_email_address_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX contacts_link_id_email_address_idx ON public.email_contacts USING btree (link_id, email_address);


--
-- Name: crm_comment_thread_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_comment_thread_id_created_at_idx ON public.crm_comment USING btree (thread_id, created_at);


--
-- Name: crm_companies_team_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_companies_team_id_idx ON public.crm_companies USING btree (team_id);


--
-- Name: crm_companies_visible_team_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_companies_visible_team_id_idx ON public.crm_companies USING btree (team_id) WHERE (hidden = false);


--
-- Name: crm_contact_sources_contact_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_contact_sources_contact_id_idx ON public.crm_contact_sources USING btree (contact_id);


--
-- Name: crm_contact_sources_link_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_contact_sources_link_id_idx ON public.crm_contact_sources USING btree (link_id);


--
-- Name: crm_contacts_company_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_contacts_company_id_idx ON public.crm_contacts USING btree (company_id);


--
-- Name: crm_contacts_email_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_contacts_email_idx ON public.crm_contacts USING btree (email);


--
-- Name: crm_contacts_visible_company_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_contacts_visible_company_id_idx ON public.crm_contacts USING btree (company_id) WHERE (hidden = false);


--
-- Name: crm_domain_directory_domain_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX crm_domain_directory_domain_key ON public.crm_domain_directory USING btree (lower(domain));


--
-- Name: crm_domains_company_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_domains_company_id_idx ON public.crm_domains USING btree (company_id);


--
-- Name: crm_domains_domain_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_domains_domain_idx ON public.crm_domains USING btree (domain);


--
-- Name: crm_domains_team_id_lower_domain_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX crm_domains_team_id_lower_domain_unique ON public.crm_domains USING btree (team_id, lower(domain));


--
-- Name: crm_thread_company_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_thread_company_id_idx ON public.crm_thread USING btree (company_id) WHERE (company_id IS NOT NULL);


--
-- Name: crm_thread_contact_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX crm_thread_contact_id_idx ON public.crm_thread USING btree (contact_id) WHERE (contact_id IS NOT NULL);


--
-- Name: email_filters_link_id_email_address_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX email_filters_link_id_email_address_uq ON public.email_filters USING btree (link_id, lower((email_address)::text)) WHERE (email_address IS NOT NULL);


--
-- Name: email_filters_link_id_email_domain_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX email_filters_link_id_email_domain_uq ON public.email_filters USING btree (link_id, lower((email_domain)::text)) WHERE (email_domain IS NOT NULL);


--
-- Name: entity_access_entity_id_entity_type_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX entity_access_entity_id_entity_type_idx ON public.entity_access USING btree (entity_id, entity_type);


--
-- Name: entity_access_granted_from_project_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX entity_access_granted_from_project_id_idx ON public.entity_access USING btree (granted_from_project_id);


--
-- Name: entity_access_unique_with_project; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX entity_access_unique_with_project ON public.entity_access USING btree (entity_id, entity_type, source_id, source_type, granted_from_project_id) WHERE (granted_from_project_id IS NOT NULL);


--
-- Name: entity_access_unique_without_project; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX entity_access_unique_without_project ON public.entity_access USING btree (entity_id, entity_type, source_id, source_type) WHERE (granted_from_project_id IS NULL);


--
-- Name: favorite_entity_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX favorite_entity_idx ON public.favorite USING btree (entity_type, entity_id);


--
-- Name: favorite_user_sort_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX favorite_user_sort_idx ON public.favorite USING btree (user_id, sort_order);


--
-- Name: github_app_installation_id_source_type_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX github_app_installation_id_source_type_idx ON public.github_app_installation USING btree (id, source_type);


--
-- Name: github_app_installation_installer_github_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX github_app_installation_installer_github_user_id_idx ON public.github_app_installation_installer USING btree (github_user_id);


--
-- Name: github_app_installation_source_type_source_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX github_app_installation_source_type_source_id_idx ON public.github_app_installation USING btree (source_type, source_id);


--
-- Name: hearth_user_email_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX hearth_user_email_key ON public.hearth_user USING btree (email);


--
-- Name: hearth_user_email_verification_email_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX hearth_user_email_verification_email_key ON public.hearth_user_email_verification USING btree (email);


--
-- Name: hearth_user_email_verification_hearth_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hearth_user_email_verification_hearth_user_id_idx ON public.hearth_user_email_verification USING btree (hearth_user_id);


--
-- Name: hearth_user_links_child_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hearth_user_links_child_idx ON public.hearth_user_links USING btree (child_hearth_id);


--
-- Name: hearth_user_links_link_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hearth_user_links_link_idx ON public.hearth_user_links USING btree (link_id);


--
-- Name: hearth_user_stripe_customer_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX hearth_user_stripe_customer_id_key ON public.hearth_user USING btree (stripe_customer_id);


--
-- Name: hearth_user_username_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX hearth_user_username_key ON public.hearth_user USING btree (username);


--
-- Name: id_mapping_target_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX id_mapping_target_id_idx ON public.id_mapping USING btree (target_id);


--
-- Name: idx_call_record_transcripts_call_record_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_record_transcripts_call_record_id ON public.call_record_transcripts USING btree (call_record_id);


--
-- Name: idx_call_record_transcripts_voice_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_record_transcripts_voice_id ON public.call_record_transcripts USING btree (voice_id);


--
-- Name: idx_call_records_channel_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_records_channel_id ON public.call_records USING btree (channel_id);


--
-- Name: idx_call_records_egress_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_records_egress_id ON public.call_records USING btree (egress_id);


--
-- Name: idx_call_records_started_at_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_records_started_at_id ON public.call_records USING btree (started_at, id);


--
-- Name: idx_call_transcripts_call_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_transcripts_call_id ON public.call_transcripts USING btree (call_id);


--
-- Name: idx_calls_channel_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_calls_channel_id ON public.calls USING btree (channel_id);


--
-- Name: idx_calls_room_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_calls_room_name ON public.calls USING btree (room_name);


--
-- Name: idx_channel_notification_email_sent_channel; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_notification_email_sent_channel ON public.channel_notification_email_sent USING btree (channel_id);


--
-- Name: idx_channel_notification_email_sent_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_channel_notification_email_sent_user ON public.channel_notification_email_sent USING btree (user_id);


--
-- Name: idx_chat_live_id_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chat_live_id_user ON public."Chat" USING btree (id) INCLUDE ("userId") WHERE ("deletedAt" IS NULL);


--
-- Name: idx_chat_message_updated_at_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chat_message_updated_at_id ON public."ChatMessage" USING btree ("updatedAt", id);


--
-- Name: idx_chat_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chat_name_trgm ON public."Chat" USING gin (name public.gin_trgm_ops);


--
-- Name: idx_comms_activity_channel_id_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_activity_channel_id_user_id ON public.comms_activity USING btree (user_id, channel_id);


--
-- Name: idx_comms_activity_channel_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_activity_channel_user ON public.comms_activity USING btree (channel_id, user_id);


--
-- Name: idx_comms_activity_user_times; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_activity_user_times ON public.comms_activity USING btree (user_id, updated_at, created_at);


--
-- Name: idx_comms_attachments_channel_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_attachments_channel_created ON public.comms_attachments USING btree (channel_id, created_at);


--
-- Name: idx_comms_attachments_channel_cursor; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_attachments_channel_cursor ON public.comms_attachments USING btree (channel_id, created_at DESC, id DESC);


--
-- Name: idx_comms_attachments_channel_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_attachments_channel_id ON public.comms_attachments USING btree (channel_id);


--
-- Name: idx_comms_attachments_entity_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_attachments_entity_created ON public.comms_attachments USING btree (entity_type, entity_id, created_at DESC) INCLUDE (channel_id, message_id);


--
-- Name: idx_comms_attachments_message_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_attachments_message_id ON public.comms_attachments USING btree (message_id);


--
-- Name: idx_comms_channels_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_channels_team_id ON public.comms_channels USING btree (team_id) WHERE (team_id IS NOT NULL);


--
-- Name: idx_comms_cp_active_by_channel_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_cp_active_by_channel_user ON public.comms_channel_participants USING btree (channel_id, user_id) WHERE (left_at IS NULL);


--
-- Name: idx_comms_cp_active_by_user_channel; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_cp_active_by_user_channel ON public.comms_channel_participants USING btree (user_id, channel_id) WHERE (left_at IS NULL);


--
-- Name: idx_comms_entity_mentions_combination; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_entity_mentions_combination ON public.comms_entity_mentions USING btree (source_entity_type, source_entity_id, entity_type, entity_id);


--
-- Name: idx_comms_entity_mentions_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_entity_mentions_created_at ON public.comms_entity_mentions USING btree (created_at DESC);


--
-- Name: idx_comms_entity_mentions_entity_type_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_entity_mentions_entity_type_id ON public.comms_entity_mentions USING btree (entity_type, entity_id) INCLUDE (source_entity_type, source_entity_id);


--
-- Name: idx_comms_entity_mentions_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_entity_mentions_source ON public.comms_entity_mentions USING btree (source_entity_type, source_entity_id);


--
-- Name: idx_comms_entity_mentions_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_entity_mentions_user_id ON public.comms_entity_mentions USING btree (user_id) WHERE (user_id IS NOT NULL);


--
-- Name: idx_comms_messages_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_messages_active ON public.comms_messages USING btree (id) WHERE (deleted_at IS NULL);


--
-- Name: idx_comms_messages_channel_created_at_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_messages_channel_created_at_active ON public.comms_messages USING btree (channel_id, created_at DESC) WHERE (deleted_at IS NULL);


--
-- Name: idx_comms_messages_channel_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_messages_channel_id ON public.comms_messages USING btree (channel_id);


--
-- Name: idx_comms_messages_channel_timeline; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_messages_channel_timeline ON public.comms_messages USING btree (channel_id, created_at);


--
-- Name: idx_comms_messages_channel_toplevel_cursor; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_messages_channel_toplevel_cursor ON public.comms_messages USING btree (channel_id, created_at DESC, id DESC) WHERE (thread_id IS NULL);


--
-- Name: idx_comms_messages_created_at_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_messages_created_at_active ON public.comms_messages USING btree (created_at DESC) WHERE (deleted_at IS NULL);


--
-- Name: idx_comms_messages_sender_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_messages_sender_id ON public.comms_messages USING btree (sender_id);


--
-- Name: idx_comms_messages_thread_active_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_messages_thread_active_created ON public.comms_messages USING btree (thread_id, created_at DESC) WHERE ((thread_id IS NOT NULL) AND (deleted_at IS NULL));


--
-- Name: idx_comms_messages_thread_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_messages_thread_id ON public.comms_messages USING btree (thread_id) WHERE (thread_id IS NOT NULL);


--
-- Name: idx_comms_reactions_message_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_comms_reactions_message_id ON public.comms_reactions USING btree (message_id);


--
-- Name: idx_contacts_backfill_outbox_applied_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_contacts_backfill_outbox_applied_at ON public.contacts_backfill_outbox USING btree (applied_at);


--
-- Name: idx_contacts_connections_user1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_contacts_connections_user1 ON public.contacts_connections USING btree (user1);


--
-- Name: idx_contacts_connections_user2; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_contacts_connections_user2 ON public.contacts_connections USING btree (user2);


--
-- Name: idx_document_email_attachment_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_document_email_attachment_id ON public.document_email USING btree (email_attachment_id);


--
-- Name: idx_document_id_covering; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_document_id_covering ON public."Document" USING btree (id) INCLUDE ("updatedAt", "deletedAt");


--
-- Name: idx_document_live_id_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_document_live_id_owner ON public."Document" USING btree (id) INCLUDE (owner) WHERE ("deletedAt" IS NULL);


--
-- Name: idx_document_updated_at_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_document_updated_at_id ON public."Document" USING btree ("updatedAt", id);


--
-- Name: idx_email_attachments_calendar_message_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_attachments_calendar_message_id ON public.email_attachments USING btree (message_id) WHERE (((filename)::text ~~* '%.ics'::text) OR ((mime_type)::text = 'text/calendar'::text) OR ((mime_type)::text = 'application/ics'::text));


--
-- Name: idx_email_attachments_drafts_message_db_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_attachments_drafts_message_db_id ON public.email_attachments_drafts USING btree (draft_id);


--
-- Name: idx_email_attachments_fwd_attachment_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_attachments_fwd_attachment_id ON public.email_attachments_fwd USING btree (attachment_id);


--
-- Name: idx_email_attachments_message_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_attachments_message_id ON public.email_attachments USING btree (message_id);


--
-- Name: idx_email_attachments_sfs_attachment_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_attachments_sfs_attachment_id ON public.email_attachments_sfs USING btree (attachment_id);


--
-- Name: idx_email_backfill_jobs_link_id_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_backfill_jobs_link_id_created_at ON public.email_backfill_jobs USING btree (link_id, created_at DESC);


--
-- Name: idx_email_contacts_email_address_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_contacts_email_address_trgm ON public.email_contacts USING gin (email_address public.gin_trgm_ops);


--
-- Name: idx_email_contacts_email_domain; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_contacts_email_domain ON public.email_contacts USING btree (lower(split_part((email_address)::text, '@'::text, 2)));


--
-- Name: idx_email_contacts_link_id_lower_email; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_contacts_link_id_lower_email ON public.email_contacts USING btree (link_id, lower((email_address)::text));


--
-- Name: idx_email_contacts_link_id_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_contacts_link_id_name ON public.email_contacts USING btree (link_id, name) WHERE (name IS NOT NULL);


--
-- Name: idx_email_filters_link_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_filters_link_id ON public.email_filters USING btree (link_id);


--
-- Name: idx_email_labels_link_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_labels_link_id ON public.email_labels USING btree (link_id);


--
-- Name: idx_email_labels_link_id_for_trash_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_labels_link_id_for_trash_lookup ON public.email_labels USING btree (link_id) WHERE ((name)::text = 'TRASH'::text);


--
-- Name: idx_email_labels_link_id_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_labels_link_id_name ON public.email_labels USING btree (link_id, name);


--
-- Name: idx_email_labels_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_labels_name ON public.email_labels USING btree (name);


--
-- Name: idx_email_links_active_provider_hash_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_links_active_provider_hash_bucket ON public.email_links USING btree (provider, ((abs(hashtext((id)::text)) % 24))) WHERE (is_sync_active = true);


--
-- Name: idx_email_links_email_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_links_email_provider ON public.email_links USING btree (lower((email_address)::text), provider);


--
-- Name: idx_email_links_fusionauth_user_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_links_fusionauth_user_active ON public.email_links USING btree (fusionauth_user_id, is_sync_active) WHERE (is_sync_active = true);


--
-- Name: idx_email_links_hearth_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_links_hearth_id ON public.email_links USING btree (hearth_id);


--
-- Name: idx_email_links_history_fusionauth_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_links_history_fusionauth_user_id ON public.email_links_history USING btree (fusionauth_user_id);


--
-- Name: idx_email_links_history_link_id_not_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_links_history_link_id_not_deleted ON public.email_links_history USING btree (link_id) WHERE (deleted_at IS NULL);


--
-- Name: idx_email_links_provider_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_links_provider_active ON public.email_links USING btree (provider, is_sync_active) WHERE (is_sync_active = true);


--
-- Name: idx_email_message_labels_by_label; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_message_labels_by_label ON public.email_message_labels USING btree (label_id, message_id);


--
-- Name: idx_email_message_recipients_contact_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_message_recipients_contact_id ON public.email_message_recipients USING btree (contact_id);


--
-- Name: idx_email_messages_drafts_view; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_drafts_view ON public.email_messages USING btree (link_id, thread_id, internal_date_ts DESC) WHERE (is_draft = true);


--
-- Name: idx_email_messages_from_contact_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_from_contact_id ON public.email_messages USING btree (from_contact_id);


--
-- Name: idx_email_messages_latest_content; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_latest_content ON public.email_messages USING btree (thread_id, COALESCE(internal_date_ts, sent_at, created_at) DESC, id DESC) WHERE (is_draft = false);


--
-- Name: idx_email_messages_link_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_link_id ON public.email_messages USING btree (link_id);


--
-- Name: idx_email_messages_link_id_global_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_link_id_global_id ON public.email_messages USING btree (link_id, global_id);


--
-- Name: idx_email_messages_link_id_replying_to_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_link_id_replying_to_id ON public.email_messages USING btree (link_id, replying_to_id);


--
-- Name: idx_email_messages_link_id_sent; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_link_id_sent ON public.email_messages USING btree (link_id) WHERE (is_sent = true);


--
-- Name: idx_email_messages_link_id_thread_id_date_asc; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_link_id_thread_id_date_asc ON public.email_messages USING btree (link_id, thread_id, internal_date_ts);


--
-- Name: idx_email_messages_link_thread_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_link_thread_date ON public.email_messages USING btree (link_id, thread_id, internal_date_ts DESC);


--
-- Name: idx_email_messages_replying_to_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_replying_to_id ON public.email_messages USING btree (replying_to_id) WHERE (replying_to_id IS NOT NULL);


--
-- Name: idx_email_messages_sent_view; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_sent_view ON public.email_messages USING btree (thread_id, internal_date_ts DESC) WHERE (is_sent = true);


--
-- Name: idx_email_messages_thread_date_not_draft; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_thread_date_not_draft ON public.email_messages USING btree (thread_id, internal_date_ts DESC) WHERE (is_draft = false);


--
-- Name: idx_email_messages_thread_id_effective_date_desc; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_thread_id_effective_date_desc ON public.email_messages USING btree (thread_id, COALESCE(internal_date_ts, created_at) DESC);


--
-- Name: idx_email_messages_thread_id_internal_date_asc; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_thread_id_internal_date_asc ON public.email_messages USING btree (thread_id, internal_date_ts);


--
-- Name: idx_email_messages_thread_id_internal_date_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_thread_id_internal_date_ts ON public.email_messages USING btree (thread_id, internal_date_ts DESC);


--
-- Name: idx_email_messages_thread_root_global; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_messages_thread_root_global ON public.email_messages USING btree (thread_id, internal_date_ts, id) WHERE ((is_draft = false) AND (global_id IS NOT NULL));


--
-- Name: idx_email_scheduled_messages_send_time_sent; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_scheduled_messages_send_time_sent ON public.email_scheduled_messages USING btree (send_time, sent);


--
-- Name: idx_email_sfs_mappings_destination; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_sfs_mappings_destination ON public.email_sfs_mappings USING btree (destination);


--
-- Name: idx_email_threads_calendar_view; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_threads_calendar_view ON public.email_threads USING btree (link_id, COALESCE(latest_non_spam_message_ts, updated_at) DESC, id DESC) WHERE has_calendar_attachment;


--
-- Name: idx_email_threads_inbox_view_not_null; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_threads_inbox_view_not_null ON public.email_threads USING btree (link_id, latest_inbound_message_ts DESC) WHERE ((inbox_visible = true) AND (latest_inbound_message_ts IS NOT NULL));


--
-- Name: idx_email_threads_link_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_threads_link_id ON public.email_threads USING btree (link_id);


--
-- Name: idx_email_threads_non_spam_ts_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_threads_non_spam_ts_id ON public.email_threads USING btree (link_id, latest_non_spam_message_ts DESC, id DESC) WHERE (latest_non_spam_message_ts IS NOT NULL);


--
-- Name: idx_email_threads_project_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_threads_project_id ON public.email_threads USING btree (project_id);


--
-- Name: idx_email_threads_sent_view_link_ts_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_threads_sent_view_link_ts_id ON public.email_threads USING btree (link_id, latest_outbound_message_ts DESC, id DESC) WHERE (latest_outbound_message_ts IS NOT NULL);


--
-- Name: idx_email_threads_signal_view; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_threads_signal_view ON public.email_threads USING btree (link_id, latest_inbound_message_ts DESC, id DESC) WHERE (inbox_visible AND is_signal AND (latest_inbound_message_ts IS NOT NULL));


--
-- Name: idx_email_user_history_thread_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_user_history_thread_id ON public.email_user_history USING btree (thread_id);


--
-- Name: idx_email_user_history_thread_link; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_user_history_thread_link ON public.email_user_history USING btree (thread_id, link_id);


--
-- Name: idx_entity_access_entity_text_type_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_entity_access_entity_text_type_source ON public.entity_access USING btree (((entity_id)::text), entity_type, source_id);


--
-- Name: idx_entity_access_source_type_entity_plain; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_entity_access_source_type_entity_plain ON public.entity_access USING btree (source_id, entity_type, entity_id);


--
-- Name: idx_entity_properties_entity_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_entity_properties_entity_id ON public.entity_properties USING btree (entity_id, entity_type);


--
-- Name: idx_entity_properties_property_definition_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_entity_properties_property_definition_id ON public.entity_properties USING btree (property_definition_id);


--
-- Name: idx_entity_properties_values_gin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_entity_properties_values_gin ON public.entity_properties USING gin ("values" jsonb_path_ops);


--
-- Name: idx_ep_assignee_value_gin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ep_assignee_value_gin ON public.entity_properties USING gin ((("values" -> 'value'::text)) jsonb_path_ops) WHERE (property_definition_id = '00000001-0000-0000-0000-000000000001'::uuid);


--
-- Name: idx_ep_status_value_gin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ep_status_value_gin ON public.entity_properties USING gin ((("values" -> 'value'::text))) WHERE (property_definition_id = '00000001-0000-0000-0000-000000000002'::uuid);


--
-- Name: idx_foreign_entity_foreign_entity_id_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_foreign_entity_foreign_entity_id_source ON public.foreign_entity USING btree (foreign_entity_id, foreign_entity_source);


--
-- Name: idx_foreign_entity_metadata_participant_github_user_ids; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_foreign_entity_metadata_participant_github_user_ids ON public.foreign_entity USING gin (((metadata -> 'participantGithubUserIds'::text)));


--
-- Name: idx_foreign_entity_stored_for_id_auth_entity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_foreign_entity_stored_for_id_auth_entity ON public.foreign_entity USING btree (stored_for_id, stored_for_auth_entity);


--
-- Name: idx_frecency_aggregates_entity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_frecency_aggregates_entity ON public.frecency_aggregates USING btree (user_id, entity_type, entity_id);


--
-- Name: idx_frecency_aggregates_user_score; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_frecency_aggregates_user_score ON public.frecency_aggregates USING btree (user_id, frecency_score DESC);


--
-- Name: idx_frecency_events_unprocessed; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_frecency_events_unprocessed ON public.frecency_events USING btree (was_processed) WHERE (was_processed = false);


--
-- Name: idx_frecency_events_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_frecency_events_user_id ON public.frecency_events USING btree (user_id);


--
-- Name: idx_github_links_fusionauth_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_github_links_fusionauth_user_id ON public.github_links USING btree (fusionauth_user_id);


--
-- Name: idx_github_links_github_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_github_links_github_user_id ON public.github_links USING btree (github_user_id);


--
-- Name: idx_github_links_github_username; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_github_links_github_username ON public.github_links USING btree (github_username);


--
-- Name: idx_github_links_hearth_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_github_links_hearth_id ON public.github_links USING btree (hearth_id);


--
-- Name: idx_github_pr_tasks_github_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_github_pr_tasks_github_key ON public.github_pr_tasks USING btree (github_key);


--
-- Name: idx_github_pr_tasks_task_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_github_pr_tasks_task_id ON public.github_pr_tasks USING btree (task_id);


--
-- Name: idx_github_pr_tasks_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_github_pr_tasks_team_id ON public.github_pr_tasks USING btree (team_id);


--
-- Name: idx_hearth_user_voice_voice_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_hearth_user_voice_voice_id ON public.hearth_user_voice USING btree (voice_id);


--
-- Name: idx_mcp_servers_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mcp_servers_user_id ON public.mcp_servers USING btree (user_id);


--
-- Name: idx_notification_event; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_event ON public.notification USING btree (event_item_id);


--
-- Name: idx_notification_event_item_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_event_item_type ON public.notification USING btree (event_item_type);


--
-- Name: idx_notification_event_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_event_type ON public.notification USING btree (notification_event_type);


--
-- Name: idx_notification_event_type_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_event_type_id ON public.notification USING btree (event_item_type, event_item_id);


--
-- Name: idx_notification_message_receipt_user_notification; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_message_receipt_user_notification ON public.notification_message_receipt USING btree (user_id, notification_id);


--
-- Name: idx_notification_secondary_event_type_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_secondary_event_type_id ON public.notification USING btree (secondary_event_item_type, secondary_event_item_id) WHERE (secondary_event_item_id IS NOT NULL);


--
-- Name: idx_notification_user_device_registration_device_token; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_user_device_registration_device_token ON public.notification_user_device_registration USING btree (device_token);


--
-- Name: idx_notification_user_device_registration_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_user_device_registration_user ON public.notification_user_device_registration USING btree (user_id);


--
-- Name: idx_project_live_id_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_live_id_user ON public."Project" USING btree (id) INCLUDE ("userId") WHERE ("deletedAt" IS NULL);


--
-- Name: idx_project_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_name_trgm ON public."Project" USING gin (name public.gin_trgm_ops);


--
-- Name: idx_property_definitions_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_property_definitions_team_id ON public.property_definitions USING btree (team_id) WHERE (team_id IS NOT NULL);


--
-- Name: idx_property_definitions_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_property_definitions_user_id ON public.property_definitions USING btree (user_id) WHERE (user_id IS NOT NULL);


--
-- Name: idx_property_options_property_definition_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_property_options_property_definition_id ON public.property_options USING btree (property_definition_id);


--
-- Name: idx_referral_tracking_referred_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_referral_tracking_referred_id ON public.referral_tracking USING btree (referred_id);


--
-- Name: idx_referral_tracking_referrer_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_referral_tracking_referrer_id ON public.referral_tracking USING btree (referrer_id);


--
-- Name: idx_resolved_message_content_message_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_resolved_message_content_message_id ON public.resolved_message_content USING btree ("messageId");


--
-- Name: idx_user_notif_type_pref_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_notif_type_pref_user ON public.user_notification_type_preference USING btree (user_id);


--
-- Name: idx_user_notification_active_filter; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_notification_active_filter ON public.user_notification USING btree (user_id, notification_id, done, seen_at) WHERE (deleted_at IS NULL);


--
-- Name: idx_user_notification_item_unsubscribe_item_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_notification_item_unsubscribe_item_id ON public.user_notification_item_unsubscribe USING btree (item_id);


--
-- Name: idx_user_notification_item_unsubscribe_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_notification_item_unsubscribe_user_id ON public.user_notification_item_unsubscribe USING btree (user_id);


--
-- Name: idx_user_notification_notification; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_notification_notification ON public.user_notification USING btree (notification_id);


--
-- Name: idx_user_notification_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_notification_user ON public.user_notification USING btree (user_id);


--
-- Name: idx_views_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_views_user_id ON public.saved_view USING btree (user_id);


--
-- Name: idx_voice_embedding_cosine; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_voice_embedding_cosine ON public.voice USING hnsw (embedding public.vector_cosine_ops);


--
-- Name: import_entity_team_imported_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX import_entity_team_imported_idx ON public.import_entity USING btree (team_id, source, foreign_id) WHERE ((team_id IS NOT NULL) AND (status = 'imported'::text));


--
-- Name: in_progress_email_link_email_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX in_progress_email_link_email_key ON public.in_progress_email_link USING btree (email);


--
-- Name: in_progress_user_link_hearth_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX in_progress_user_link_hearth_user_id_idx ON public.in_progress_user_link USING btree (hearth_user_id);


--
-- Name: notification_channel_message_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notification_channel_message_id_idx ON public.notification USING btree (((metadata ->> 'messageId'::text))) WHERE (event_item_type = 'channel'::text);


--
-- Name: notification_email_unsubscribe_code_code_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX notification_email_unsubscribe_code_code_idx ON public.notification_email_unsubscribe_code USING btree (code);


--
-- Name: processing_ai_projections_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX processing_ai_projections_created_at_idx ON public.processing_ai_projections USING btree (created_at);


--
-- Name: scheduled_action_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX scheduled_action_owner_idx ON public.scheduled_action USING btree (owner);


--
-- Name: task_duplicate_embedding_search_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX task_duplicate_embedding_search_key ON public.task_duplicate_embedding USING btree (search_key);


--
-- Name: task_duplicate_embedding_vector_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX task_duplicate_embedding_vector_idx ON public.task_duplicate_embedding USING hnsw (embedding public.vector_cosine_ops);


--
-- Name: task_duplicate_match_duplicate_task_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX task_duplicate_match_duplicate_task_id_idx ON public.task_duplicate_match USING btree (duplicate_task_id) WHERE (status = 'active'::text);


--
-- Name: task_duplicate_match_pair_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX task_duplicate_match_pair_idx ON public.task_duplicate_match USING btree (task_id, duplicate_task_id);


--
-- Name: task_duplicate_match_task_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX task_duplicate_match_task_id_idx ON public.task_duplicate_match USING btree (task_id) WHERE (status = 'active'::text);


--
-- Name: team_auto_join_domain_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX team_auto_join_domain_idx ON public.team USING btree (auto_join_domain) WHERE (auto_join_domain IS NOT NULL);


--
-- Name: team_invite_email_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX team_invite_email_idx ON public.team_invite USING btree (email);


--
-- Name: team_invite_email_team_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX team_invite_email_team_id_key ON public.team_invite USING btree (email, team_id);


--
-- Name: team_invite_invited_by_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX team_invite_invited_by_idx ON public.team_invite USING btree (invited_by);


--
-- Name: team_invite_team_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX team_invite_team_id_idx ON public.team_invite USING btree (team_id);


--
-- Name: team_owner_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX team_owner_id_idx ON public.team USING btree (owner_id);


--
-- Name: team_task_document_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX team_task_document_id_idx ON public.team_task USING btree (document_id);


--
-- Name: team_user_team_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX team_user_team_id_idx ON public.team_user USING btree (team_id);


--
-- Name: team_user_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX team_user_user_id_idx ON public.team_user USING btree (user_id);


--
-- Name: unique_property_definitions_team_display_name; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX unique_property_definitions_team_display_name ON public.property_definitions USING btree (team_id, display_name) WHERE ((team_id IS NOT NULL) AND (data_type <> 'TAG'::public.property_data_type));


--
-- Name: unique_property_definitions_user_display_name; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX unique_property_definitions_user_display_name ON public.property_definitions USING btree (user_id, display_name) WHERE ((user_id IS NOT NULL) AND (data_type <> 'TAG'::public.property_data_type));


--
-- Name: unique_property_options_string_value_sha256; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX unique_property_options_string_value_sha256 ON public.property_options USING btree (property_definition_id, public.digest(string_value, 'sha256'::text)) WHERE (string_value IS NOT NULL);


--
-- Name: uq_active_backfill_job_per_link; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_active_backfill_job_per_link ON public.email_backfill_jobs USING btree (link_id) WHERE (status = ANY (ARRAY['Init'::public.email_backfill_job_status, 'InProgress'::public.email_backfill_job_status]));


--
-- Name: uq_active_crm_cleanup_job; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_active_crm_cleanup_job ON public.crm_cleanup_jobs USING btree ((true)) WHERE (status = ANY (ARRAY['Init'::public.crm_cleanup_job_status, 'InProgress'::public.crm_cleanup_job_status]));


--
-- Name: uq_crm_cleanup_candidate; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_crm_cleanup_candidate ON public.crm_cleanup_candidates USING btree (link_id, contact_email);


--
-- Name: uq_github_links_hearth_id_github_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_github_links_hearth_id_github_user_id ON public.github_links USING btree (hearth_id, github_user_id);


--
-- Name: uq_messages_link_id_provider_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_messages_link_id_provider_id ON public.email_messages USING btree (link_id, provider_id) WHERE (provider_id IS NOT NULL);


--
-- Name: uq_tag_definition_per_team; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_tag_definition_per_team ON public.property_definitions USING btree (team_id) WHERE ((data_type = 'TAG'::public.property_data_type) AND (team_id IS NOT NULL));


--
-- Name: uq_tag_definition_per_user; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_tag_definition_per_user ON public.property_definitions USING btree (user_id) WHERE ((data_type = 'TAG'::public.property_data_type) AND (user_id IS NOT NULL));


--
-- Name: uq_threads_link_id_provider_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_threads_link_id_provider_id ON public.email_threads USING btree (link_id, provider_id) WHERE (provider_id IS NOT NULL);


--
-- Name: user_ai_projection_projection_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_ai_projection_projection_idx ON public.user_ai_projection USING btree (ai_projection_id);


--
-- Name: webhook_delivery_event_ordering_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_delivery_event_ordering_idx ON public.webhook_delivery USING btree (event_ordering_key);


--
-- Name: webhook_delivery_status_next_attempt_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_delivery_status_next_attempt_idx ON public.webhook_delivery USING btree (status, next_attempt_at);


--
-- Name: webhook_delivery_webhook_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_delivery_webhook_created_idx ON public.webhook_delivery USING btree (webhook_id, created_at);


--
-- Name: webhook_filters_gin_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_filters_gin_idx ON public.webhook USING gin (filters jsonb_path_ops);


--
-- Name: webhook_workspace_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_workspace_status_idx ON public.webhook USING btree (workspace_id, status, is_valid);


--
-- Name: property_definitions check_orphaned_property_definition; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER check_orphaned_property_definition BEFORE UPDATE ON public.property_definitions FOR EACH ROW EXECUTE FUNCTION public.delete_orphaned_property_definition();


--
-- Name: crm_comment crm_comment_set_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER crm_comment_set_updated_at BEFORE UPDATE ON public.crm_comment FOR EACH ROW EXECUTE FUNCTION public.set_crm_updated_at();


--
-- Name: crm_companies crm_companies_set_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER crm_companies_set_updated_at BEFORE UPDATE ON public.crm_companies FOR EACH ROW EXECUTE FUNCTION public.set_crm_updated_at();


--
-- Name: crm_contacts crm_contacts_set_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER crm_contacts_set_updated_at BEFORE UPDATE ON public.crm_contacts FOR EACH ROW EXECUTE FUNCTION public.set_crm_updated_at();


--
-- Name: crm_thread crm_thread_set_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER crm_thread_set_updated_at BEFORE UPDATE ON public.crm_thread FOR EACH ROW EXECUTE FUNCTION public.set_crm_updated_at();


--
-- Name: property_definitions prevent_system_property_name_conflict; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER prevent_system_property_name_conflict BEFORE INSERT OR UPDATE ON public.property_definitions FOR EACH ROW EXECUTE FUNCTION public.check_property_name_not_system();


--
-- Name: comms_messages trg_cascade_comms_message_delete_to_notifications; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_cascade_comms_message_delete_to_notifications AFTER UPDATE OF deleted_at ON public.comms_messages FOR EACH ROW EXECUTE FUNCTION public.cascade_comms_message_delete_to_notifications();


--
-- Name: call_records trg_delete_share_permission_call_record; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_delete_share_permission_call_record BEFORE DELETE ON public.call_records FOR EACH ROW EXECUTE FUNCTION public.delete_share_permission_on_call_record_delete();


--
-- Name: notification trg_notify_user_notification_deletes; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_user_notification_deletes BEFORE DELETE ON public.notification FOR EACH ROW EXECUTE FUNCTION public.notify_user_notification_deletes();


--
-- Name: Artifact Artifact_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Artifact"
    ADD CONSTRAINT "Artifact_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: Artifact Artifact_messageId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Artifact"
    ADD CONSTRAINT "Artifact_messageId_fkey" FOREIGN KEY ("messageId") REFERENCES public."ChatMessage"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Artifact Artifact_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Artifact"
    ADD CONSTRAINT "Artifact_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: BomPart BomPart_documentBomId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."BomPart"
    ADD CONSTRAINT "BomPart_documentBomId_fkey" FOREIGN KEY ("documentBomId") REFERENCES public."DocumentBom"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ChannelSharePermission ChannelSharePermission_share_permission_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ChannelSharePermission"
    ADD CONSTRAINT "ChannelSharePermission_share_permission_id_fkey" FOREIGN KEY (share_permission_id) REFERENCES public."SharePermission"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ChatAttachment ChatAttachment_chatId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ChatAttachment"
    ADD CONSTRAINT "ChatAttachment_chatId_fkey" FOREIGN KEY ("chatId") REFERENCES public."Chat"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ChatAttachment ChatAttachment_messageId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ChatAttachment"
    ADD CONSTRAINT "ChatAttachment_messageId_fkey" FOREIGN KEY ("messageId") REFERENCES public."ChatMessage"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ChatMessage ChatMessage_chatId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ChatMessage"
    ADD CONSTRAINT "ChatMessage_chatId_fkey" FOREIGN KEY ("chatId") REFERENCES public."Chat"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ChatPermission ChatPermission_chatId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ChatPermission"
    ADD CONSTRAINT "ChatPermission_chatId_fkey" FOREIGN KEY ("chatId") REFERENCES public."Chat"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ChatPermission ChatPermission_sharePermissionId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ChatPermission"
    ADD CONSTRAINT "ChatPermission_sharePermissionId_fkey" FOREIGN KEY ("sharePermissionId") REFERENCES public."SharePermission"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Chat Chat_projectId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Chat"
    ADD CONSTRAINT "Chat_projectId_fkey" FOREIGN KEY ("projectId") REFERENCES public."Project"(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: Chat Chat_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Chat"
    ADD CONSTRAINT "Chat_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Comment Comment_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Comment"
    ADD CONSTRAINT "Comment_owner_fkey" FOREIGN KEY (owner) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Comment Comment_threadId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Comment"
    ADD CONSTRAINT "Comment_threadId_fkey" FOREIGN KEY ("threadId") REFERENCES public."Thread"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentBom DocumentBom_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentBom"
    ADD CONSTRAINT "DocumentBom_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentInstanceModificationData DocumentInstanceModificationData_documentInstanceId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentInstanceModificationData"
    ADD CONSTRAINT "DocumentInstanceModificationData_documentInstanceId_fkey" FOREIGN KEY ("documentInstanceId") REFERENCES public."DocumentInstance"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentInstance DocumentInstance_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentInstance"
    ADD CONSTRAINT "DocumentInstance_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentPermission DocumentPermission_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentPermission"
    ADD CONSTRAINT "DocumentPermission_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentPermission DocumentPermission_sharePermissionId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentPermission"
    ADD CONSTRAINT "DocumentPermission_sharePermissionId_fkey" FOREIGN KEY ("sharePermissionId") REFERENCES public."SharePermission"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentProcessResult DocumentProcessResult_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentProcessResult"
    ADD CONSTRAINT "DocumentProcessResult_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentSummary DocumentSummary_document_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentSummary"
    ADD CONSTRAINT "DocumentSummary_document_id_fkey" FOREIGN KEY (document_id) REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentTextParts DocumentTextParts_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentTextParts"
    ADD CONSTRAINT "DocumentTextParts_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentText DocumentText_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentText"
    ADD CONSTRAINT "DocumentText_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentView DocumentView_document_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentView"
    ADD CONSTRAINT "DocumentView_document_id_fkey" FOREIGN KEY (document_id) REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: DocumentView DocumentView_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."DocumentView"
    ADD CONSTRAINT "DocumentView_user_id_fkey" FOREIGN KEY (user_id) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Document Document_documentFamilyId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Document"
    ADD CONSTRAINT "Document_documentFamilyId_fkey" FOREIGN KEY ("documentFamilyId") REFERENCES public."DocumentFamily"(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: Document Document_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Document"
    ADD CONSTRAINT "Document_owner_fkey" FOREIGN KEY (owner) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Document Document_projectId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Document"
    ADD CONSTRAINT "Document_projectId_fkey" FOREIGN KEY ("projectId") REFERENCES public."Project"(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: EmailInsightsBackfillBatch EmailInsightsBackfillBatch_insightsBackfillJobId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EmailInsightsBackfillBatch"
    ADD CONSTRAINT "EmailInsightsBackfillBatch_insightsBackfillJobId_fkey" FOREIGN KEY ("insightsBackfillJobId") REFERENCES public."EmailInsightsBackfillJob"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: EmailInsightsBackfillJob EmailInsightsBackfillJob_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EmailInsightsBackfillJob"
    ADD CONSTRAINT "EmailInsightsBackfillJob_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: EmailThreadPermission EmailThreadPermission_projectId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EmailThreadPermission"
    ADD CONSTRAINT "EmailThreadPermission_projectId_fkey" FOREIGN KEY ("projectId") REFERENCES public."Project"(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: EmailThreadPermission EmailThreadPermission_sharePermissionId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EmailThreadPermission"
    ADD CONSTRAINT "EmailThreadPermission_sharePermissionId_fkey" FOREIGN KEY ("sharePermissionId") REFERENCES public."SharePermission"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: EnterpriseEmailContacts EnterpriseEmailContacts_organizationId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EnterpriseEmailContacts"
    ADD CONSTRAINT "EnterpriseEmailContacts_organizationId_fkey" FOREIGN KEY ("organizationId") REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: EnterpriseIManageTenants EnterpriseIManageTenants_organizationId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EnterpriseIManageTenants"
    ADD CONSTRAINT "EnterpriseIManageTenants_organizationId_fkey" FOREIGN KEY ("organizationId") REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: EnterpriseRules EnterpriseRules_organizationId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."EnterpriseRules"
    ADD CONSTRAINT "EnterpriseRules_organizationId_fkey" FOREIGN KEY ("organizationId") REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ExperimentLog ExperimentLog_experiment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ExperimentLog"
    ADD CONSTRAINT "ExperimentLog_experiment_id_fkey" FOREIGN KEY (experiment_id) REFERENCES public."Experiment"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ExperimentLog ExperimentLog_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ExperimentLog"
    ADD CONSTRAINT "ExperimentLog_user_id_fkey" FOREIGN KEY (user_id) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: HearthPromptAttachment HearthPromptAttachment_hearth_prompt_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."HearthPromptAttachment"
    ADD CONSTRAINT "HearthPromptAttachment_hearth_prompt_id_fkey" FOREIGN KEY (hearth_prompt_id) REFERENCES public."HearthPrompt"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: HearthPromptPermission HearthPromptPermission_hearth_prompt_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."HearthPromptPermission"
    ADD CONSTRAINT "HearthPromptPermission_hearth_prompt_id_fkey" FOREIGN KEY (hearth_prompt_id) REFERENCES public."HearthPrompt"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: HearthPromptPermission HearthPromptPermission_share_permission_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."HearthPromptPermission"
    ADD CONSTRAINT "HearthPromptPermission_share_permission_id_fkey" FOREIGN KEY (share_permission_id) REFERENCES public."SharePermission"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: HearthPrompt HearthPrompt_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."HearthPrompt"
    ADD CONSTRAINT "HearthPrompt_user_id_fkey" FOREIGN KEY (user_id) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: InsightContext InsightContext_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."InsightContext"
    ADD CONSTRAINT "InsightContext_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: InstructionsDocuments InstructionsDocuments_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."InstructionsDocuments"
    ADD CONSTRAINT "InstructionsDocuments_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: InstructionsDocuments InstructionsDocuments_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."InstructionsDocuments"
    ADD CONSTRAINT "InstructionsDocuments_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: JobToDocumentProcessResult JobToDocumentProcessResult_documentProcessResultId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."JobToDocumentProcessResult"
    ADD CONSTRAINT "JobToDocumentProcessResult_documentProcessResultId_fkey" FOREIGN KEY ("documentProcessResultId") REFERENCES public."DocumentProcessResult"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: OrganizationBilling OrganizationBilling_organizationId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationBilling"
    ADD CONSTRAINT "OrganizationBilling_organizationId_fkey" FOREIGN KEY ("organizationId") REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: OrganizationDefaultSharePermission OrganizationDefaultSharePermission_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationDefaultSharePermission"
    ADD CONSTRAINT "OrganizationDefaultSharePermission_organization_id_fkey" FOREIGN KEY (organization_id) REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: OrganizationEmailMatches OrganizationEmailMatches_organizationId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationEmailMatches"
    ADD CONSTRAINT "OrganizationEmailMatches_organizationId_fkey" FOREIGN KEY ("organizationId") REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: OrganizationIT OrganizationIT_organizationId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationIT"
    ADD CONSTRAINT "OrganizationIT_organizationId_fkey" FOREIGN KEY ("organizationId") REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: OrganizationInvitation OrganizationInvitation_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationInvitation"
    ADD CONSTRAINT "OrganizationInvitation_organization_id_fkey" FOREIGN KEY (organization_id) REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: OrganizationItJob OrganizationItJob_organizationId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationItJob"
    ADD CONSTRAINT "OrganizationItJob_organizationId_fkey" FOREIGN KEY ("organizationId") REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: OrganizationRetentionPolicy OrganizationRetentionPolicy_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."OrganizationRetentionPolicy"
    ADD CONSTRAINT "OrganizationRetentionPolicy_organization_id_fkey" FOREIGN KEY (organization_id) REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: PdfHighlightAnchor PdfHighlightAnchor_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfHighlightAnchor"
    ADD CONSTRAINT "PdfHighlightAnchor_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: PdfHighlightAnchor PdfHighlightAnchor_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfHighlightAnchor"
    ADD CONSTRAINT "PdfHighlightAnchor_owner_fkey" FOREIGN KEY (owner) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: PdfHighlightAnchor PdfHighlightAnchor_threadId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfHighlightAnchor"
    ADD CONSTRAINT "PdfHighlightAnchor_threadId_fkey" FOREIGN KEY ("threadId") REFERENCES public."Thread"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: PdfHighlightRect PdfHighlightRect_pdfHighlightAnchorId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfHighlightRect"
    ADD CONSTRAINT "PdfHighlightRect_pdfHighlightAnchorId_fkey" FOREIGN KEY ("pdfHighlightAnchorId") REFERENCES public."PdfHighlightAnchor"(uuid) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: PdfPlaceableCommentAnchor PdfPlaceableCommentAnchor_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfPlaceableCommentAnchor"
    ADD CONSTRAINT "PdfPlaceableCommentAnchor_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: PdfPlaceableCommentAnchor PdfPlaceableCommentAnchor_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfPlaceableCommentAnchor"
    ADD CONSTRAINT "PdfPlaceableCommentAnchor_owner_fkey" FOREIGN KEY (owner) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: PdfPlaceableCommentAnchor PdfPlaceableCommentAnchor_threadId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."PdfPlaceableCommentAnchor"
    ADD CONSTRAINT "PdfPlaceableCommentAnchor_threadId_fkey" FOREIGN KEY ("threadId") REFERENCES public."Thread"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Pin Pin_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Pin"
    ADD CONSTRAINT "Pin_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ProjectPermission ProjectPermission_projectId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ProjectPermission"
    ADD CONSTRAINT "ProjectPermission_projectId_fkey" FOREIGN KEY ("projectId") REFERENCES public."Project"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ProjectPermission ProjectPermission_sharePermissionId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ProjectPermission"
    ADD CONSTRAINT "ProjectPermission_sharePermissionId_fkey" FOREIGN KEY ("sharePermissionId") REFERENCES public."SharePermission"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Project Project_parentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Project"
    ADD CONSTRAINT "Project_parentId_fkey" FOREIGN KEY ("parentId") REFERENCES public."Project"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Project Project_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Project"
    ADD CONSTRAINT "Project_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: RolesOnOrganizations RolesOnOrganizations_organizationId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."RolesOnOrganizations"
    ADD CONSTRAINT "RolesOnOrganizations_organizationId_fkey" FOREIGN KEY ("organizationId") REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: RolesOnOrganizations RolesOnOrganizations_roleId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."RolesOnOrganizations"
    ADD CONSTRAINT "RolesOnOrganizations_roleId_fkey" FOREIGN KEY ("roleId") REFERENCES public."Role"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: RolesOnPermissions RolesOnPermissions_permissionId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."RolesOnPermissions"
    ADD CONSTRAINT "RolesOnPermissions_permissionId_fkey" FOREIGN KEY ("permissionId") REFERENCES public."Permission"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: RolesOnPermissions RolesOnPermissions_roleId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."RolesOnPermissions"
    ADD CONSTRAINT "RolesOnPermissions_roleId_fkey" FOREIGN KEY ("roleId") REFERENCES public."Role"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: RolesOnUsers RolesOnUsers_roleId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."RolesOnUsers"
    ADD CONSTRAINT "RolesOnUsers_roleId_fkey" FOREIGN KEY ("roleId") REFERENCES public."Role"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: RolesOnUsers RolesOnUsers_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."RolesOnUsers"
    ADD CONSTRAINT "RolesOnUsers_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: ThreadAnchor ThreadAnchor_threadId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."ThreadAnchor"
    ADD CONSTRAINT "ThreadAnchor_threadId_fkey" FOREIGN KEY ("threadId") REFERENCES public."Thread"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Thread Thread_documentId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Thread"
    ADD CONSTRAINT "Thread_documentId_fkey" FOREIGN KEY ("documentId") REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: Thread Thread_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."Thread"
    ADD CONSTRAINT "Thread_owner_fkey" FOREIGN KEY (owner) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: UserApiKey UserApiKey_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserApiKey"
    ADD CONSTRAINT "UserApiKey_user_id_fkey" FOREIGN KEY (user_id) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: UserDocumentViewLocation UserDocumentViewLocation_document_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserDocumentViewLocation"
    ADD CONSTRAINT "UserDocumentViewLocation_document_id_fkey" FOREIGN KEY (document_id) REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: UserDocumentViewLocation UserDocumentViewLocation_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserDocumentViewLocation"
    ADD CONSTRAINT "UserDocumentViewLocation_user_id_fkey" FOREIGN KEY (user_id) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: UserHistory UserHistory_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserHistory"
    ADD CONSTRAINT "UserHistory_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: UserInsightBatch UserInsightBatch_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserInsightBatch"
    ADD CONSTRAINT "UserInsightBatch_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: UserInsights UserInsights_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."UserInsights"
    ADD CONSTRAINT "UserInsights_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: User User_hearth_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."User"
    ADD CONSTRAINT "User_hearth_user_id_fkey" FOREIGN KEY (hearth_user_id) REFERENCES public.hearth_user(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: User User_organizationId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."User"
    ADD CONSTRAINT "User_organizationId_fkey" FOREIGN KEY ("organizationId") REFERENCES public."Organization"(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: WebAnnotations WebAnnotations_chatId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."WebAnnotations"
    ADD CONSTRAINT "WebAnnotations_chatId_fkey" FOREIGN KEY ("chatId") REFERENCES public."Chat"(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: WebAnnotations WebAnnotations_messageId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."WebAnnotations"
    ADD CONSTRAINT "WebAnnotations_messageId_fkey" FOREIGN KEY ("messageId") REFERENCES public."ChatMessage"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: WebsocketConnectionPermissions WebsocketConnectionPermissions_userId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."WebsocketConnectionPermissions"
    ADD CONSTRAINT "WebsocketConnectionPermissions_userId_fkey" FOREIGN KEY ("userId") REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: account_merge_request account_merge_request_hearth_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_merge_request
    ADD CONSTRAINT account_merge_request_hearth_user_id_fkey FOREIGN KEY (hearth_user_id) REFERENCES public.hearth_user(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: action_execution_record action_execution_record_action_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.action_execution_record
    ADD CONSTRAINT action_execution_record_action_id_fkey FOREIGN KEY (action_id) REFERENCES public.scheduled_action(id) ON DELETE CASCADE;


--
-- Name: bot_tokens bot_tokens_bot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_tokens
    ADD CONSTRAINT bot_tokens_bot_id_fkey FOREIGN KEY (bot_id) REFERENCES public.bots(id) ON DELETE CASCADE;


--
-- Name: bots bots_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON DELETE CASCADE;


--
-- Name: call_participants call_participants_call_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_participants
    ADD CONSTRAINT call_participants_call_id_fkey FOREIGN KEY (call_id) REFERENCES public.calls(id) ON DELETE CASCADE;


--
-- Name: call_record_participants call_record_participants_call_record_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_record_participants
    ADD CONSTRAINT call_record_participants_call_record_id_fkey FOREIGN KEY (call_record_id) REFERENCES public.call_records(id) ON DELETE CASCADE;


--
-- Name: call_record_transcripts call_record_transcripts_call_record_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_record_transcripts
    ADD CONSTRAINT call_record_transcripts_call_record_id_fkey FOREIGN KEY (call_record_id) REFERENCES public.call_records(id) ON DELETE CASCADE;


--
-- Name: call_record_transcripts call_record_transcripts_voice_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_record_transcripts
    ADD CONSTRAINT call_record_transcripts_voice_id_fkey FOREIGN KEY (voice_id) REFERENCES public.voice(id) ON DELETE SET NULL;


--
-- Name: call_records call_records_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_records
    ADD CONSTRAINT call_records_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.comms_channels(id) ON DELETE CASCADE;


--
-- Name: call_records call_records_share_permission_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_records
    ADD CONSTRAINT call_records_share_permission_id_fkey FOREIGN KEY (share_permission_id) REFERENCES public."SharePermission"(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: call_transcripts call_transcripts_call_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_transcripts
    ADD CONSTRAINT call_transcripts_call_id_fkey FOREIGN KEY (call_id) REFERENCES public.calls(id) ON DELETE CASCADE;


--
-- Name: call_transcripts call_transcripts_voice_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.call_transcripts
    ADD CONSTRAINT call_transcripts_voice_id_fkey FOREIGN KEY (voice_id) REFERENCES public.voice(id) ON DELETE SET NULL;


--
-- Name: calls calls_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.calls
    ADD CONSTRAINT calls_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.comms_channels(id) ON DELETE CASCADE;


--
-- Name: calls calls_share_permission_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.calls
    ADD CONSTRAINT calls_share_permission_id_fkey FOREIGN KEY (share_permission_id) REFERENCES public."SharePermission"(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: comms_attachments comms_attachments_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_attachments
    ADD CONSTRAINT comms_attachments_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.comms_channels(id) ON DELETE CASCADE;


--
-- Name: comms_attachments comms_attachments_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_attachments
    ADD CONSTRAINT comms_attachments_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.comms_messages(id) ON DELETE CASCADE;


--
-- Name: comms_channel_participants comms_channel_participants_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_channel_participants
    ADD CONSTRAINT comms_channel_participants_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.comms_channels(id) ON DELETE CASCADE;


--
-- Name: comms_channels comms_channels_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_channels
    ADD CONSTRAINT comms_channels_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON DELETE CASCADE;


--
-- Name: comms_messages comms_messages_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_messages
    ADD CONSTRAINT comms_messages_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.comms_channels(id) ON DELETE CASCADE;


--
-- Name: comms_messages comms_messages_thread_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_messages
    ADD CONSTRAINT comms_messages_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES public.comms_messages(id) ON DELETE CASCADE;


--
-- Name: comms_reactions comms_reactions_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comms_reactions
    ADD CONSTRAINT comms_reactions_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.comms_messages(id) ON DELETE CASCADE;


--
-- Name: contacts_backfill_outbox contacts_backfill_outbox_comms_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts_backfill_outbox
    ADD CONSTRAINT contacts_backfill_outbox_comms_channel_id_fkey FOREIGN KEY (comms_channel_id) REFERENCES public.comms_channels(id);


--
-- Name: crm_cleanup_candidates crm_cleanup_candidates_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_cleanup_candidates
    ADD CONSTRAINT crm_cleanup_candidates_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: crm_comment crm_comment_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_comment
    ADD CONSTRAINT crm_comment_owner_fkey FOREIGN KEY (owner) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: crm_comment crm_comment_thread_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_comment
    ADD CONSTRAINT crm_comment_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES public.crm_thread(id) ON DELETE CASCADE;


--
-- Name: crm_companies crm_companies_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_companies
    ADD CONSTRAINT crm_companies_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON DELETE CASCADE;


--
-- Name: crm_contact_sources crm_contact_sources_contact_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_contact_sources
    ADD CONSTRAINT crm_contact_sources_contact_id_fkey FOREIGN KEY (contact_id) REFERENCES public.crm_contacts(id) ON DELETE CASCADE;


--
-- Name: crm_contact_sources crm_contact_sources_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_contact_sources
    ADD CONSTRAINT crm_contact_sources_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: crm_contacts crm_contacts_company_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_contacts
    ADD CONSTRAINT crm_contacts_company_id_fkey FOREIGN KEY (company_id) REFERENCES public.crm_companies(id) ON DELETE CASCADE;


--
-- Name: crm_domains crm_domains_company_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_domains
    ADD CONSTRAINT crm_domains_company_id_fkey FOREIGN KEY (company_id) REFERENCES public.crm_companies(id) ON DELETE CASCADE;


--
-- Name: crm_domains crm_domains_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_domains
    ADD CONSTRAINT crm_domains_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON DELETE CASCADE;


--
-- Name: crm_thread crm_thread_company_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_thread
    ADD CONSTRAINT crm_thread_company_id_fkey FOREIGN KEY (company_id) REFERENCES public.crm_companies(id) ON DELETE CASCADE;


--
-- Name: crm_thread crm_thread_contact_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_thread
    ADD CONSTRAINT crm_thread_contact_id_fkey FOREIGN KEY (contact_id) REFERENCES public.crm_contacts(id) ON DELETE CASCADE;


--
-- Name: crm_thread crm_thread_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.crm_thread
    ADD CONSTRAINT crm_thread_owner_fkey FOREIGN KEY (owner) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: document_email document_email_document_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.document_email
    ADD CONSTRAINT document_email_document_id_fkey FOREIGN KEY (document_id) REFERENCES public."Document"(id) ON DELETE CASCADE;


--
-- Name: document_email document_email_email_attachment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.document_email
    ADD CONSTRAINT document_email_email_attachment_id_fkey FOREIGN KEY (email_attachment_id) REFERENCES public.email_attachments(id) ON DELETE CASCADE;


--
-- Name: document_sub_type document_sub_type_document_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.document_sub_type
    ADD CONSTRAINT document_sub_type_document_id_fkey FOREIGN KEY (document_id) REFERENCES public."Document"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: email_attachments_drafts email_attachments_drafts_draft_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments_drafts
    ADD CONSTRAINT email_attachments_drafts_draft_id_fkey FOREIGN KEY (draft_id) REFERENCES public.email_messages(id) ON DELETE CASCADE;


--
-- Name: email_attachments_fwd email_attachments_fwd_attachment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments_fwd
    ADD CONSTRAINT email_attachments_fwd_attachment_id_fkey FOREIGN KEY (attachment_id) REFERENCES public.email_attachments(id) ON DELETE CASCADE;


--
-- Name: email_attachments_fwd email_attachments_fwd_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments_fwd
    ADD CONSTRAINT email_attachments_fwd_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.email_messages(id) ON DELETE CASCADE;


--
-- Name: email_attachments email_attachments_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments
    ADD CONSTRAINT email_attachments_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.email_messages(id) ON DELETE CASCADE;


--
-- Name: email_attachments_sfs email_attachments_sfs_attachment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_attachments_sfs
    ADD CONSTRAINT email_attachments_sfs_attachment_id_fkey FOREIGN KEY (attachment_id) REFERENCES public.email_attachments(id) ON DELETE SET NULL;


--
-- Name: email_backfill_jobs email_backfill_job_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_backfill_jobs
    ADD CONSTRAINT email_backfill_job_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE SET NULL;


--
-- Name: email_contacts email_contacts_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_contacts
    ADD CONSTRAINT email_contacts_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: email_filters email_filters_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_filters
    ADD CONSTRAINT email_filters_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: email_sync_tokens email_fk_sync_tokens_link_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_sync_tokens
    ADD CONSTRAINT email_fk_sync_tokens_link_id FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: email_gmail_histories email_gmail_histories_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_gmail_histories
    ADD CONSTRAINT email_gmail_histories_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: email_labels email_labels_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_labels
    ADD CONSTRAINT email_labels_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: email_message_labels email_message_labels_label_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_message_labels
    ADD CONSTRAINT email_message_labels_label_id_fkey FOREIGN KEY (label_id) REFERENCES public.email_labels(id) ON DELETE CASCADE;


--
-- Name: email_message_labels email_message_labels_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_message_labels
    ADD CONSTRAINT email_message_labels_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.email_messages(id) ON DELETE CASCADE;


--
-- Name: email_message_recipients email_message_recipients_contact_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_message_recipients
    ADD CONSTRAINT email_message_recipients_contact_id_fkey FOREIGN KEY (contact_id) REFERENCES public.email_contacts(id) ON DELETE CASCADE;


--
-- Name: email_message_recipients email_message_recipients_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_message_recipients
    ADD CONSTRAINT email_message_recipients_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.email_messages(id) ON DELETE CASCADE;


--
-- Name: email_messages email_messages_from_contact_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_messages
    ADD CONSTRAINT email_messages_from_contact_id_fkey FOREIGN KEY (from_contact_id) REFERENCES public.email_contacts(id) ON DELETE SET NULL;


--
-- Name: email_messages email_messages_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_messages
    ADD CONSTRAINT email_messages_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: email_messages email_messages_replying_to_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_messages
    ADD CONSTRAINT email_messages_replying_to_id_fkey FOREIGN KEY (replying_to_id) REFERENCES public.email_messages(id) ON DELETE SET NULL;


--
-- Name: email_messages email_messages_thread_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_messages
    ADD CONSTRAINT email_messages_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES public.email_threads(id) ON DELETE CASCADE;


--
-- Name: email_scheduled_messages email_scheduled_messages_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_scheduled_messages
    ADD CONSTRAINT email_scheduled_messages_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: email_scheduled_messages email_scheduled_messages_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_scheduled_messages
    ADD CONSTRAINT email_scheduled_messages_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.email_messages(id) ON DELETE CASCADE;


--
-- Name: email_settings email_settings_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_settings
    ADD CONSTRAINT email_settings_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: email_threads email_threads_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_threads
    ADD CONSTRAINT email_threads_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: email_threads email_threads_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_threads
    ADD CONSTRAINT email_threads_project_id_fkey FOREIGN KEY (project_id) REFERENCES public."Project"(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: email_user_history email_user_history_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_user_history
    ADD CONSTRAINT email_user_history_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: email_user_history email_user_history_thread_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_user_history
    ADD CONSTRAINT email_user_history_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES public.email_threads(id) ON DELETE CASCADE;


--
-- Name: entity_access entity_access_granted_from_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_access
    ADD CONSTRAINT entity_access_granted_from_project_id_fkey FOREIGN KEY (granted_from_project_id) REFERENCES public."Project"(id) ON DELETE CASCADE;


--
-- Name: entity_properties entity_properties_property_definition_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_properties
    ADD CONSTRAINT entity_properties_property_definition_id_fkey FOREIGN KEY (property_definition_id) REFERENCES public.property_definitions(id) ON DELETE CASCADE;


--
-- Name: excluded_default_view excluded_default_view_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.excluded_default_view
    ADD CONSTRAINT excluded_default_view_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: favorite favorite_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.favorite
    ADD CONSTRAINT favorite_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."User"(id) ON DELETE CASCADE;


--
-- Name: notification_message_receipt fk_user_notification; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_message_receipt
    ADD CONSTRAINT fk_user_notification FOREIGN KEY (user_id, notification_id) REFERENCES public.user_notification(user_id, notification_id) ON DELETE CASCADE;


--
-- Name: github_links github_links_hearth_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_links
    ADD CONSTRAINT github_links_hearth_id_fkey FOREIGN KEY (hearth_id) REFERENCES public."User"(id) ON DELETE CASCADE;


--
-- Name: github_pr_tasks github_pr_tasks_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_pr_tasks
    ADD CONSTRAINT github_pr_tasks_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON DELETE SET NULL;


--
-- Name: hearth_user_email_verification hearth_user_email_verification_hearth_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_email_verification
    ADD CONSTRAINT hearth_user_email_verification_hearth_user_id_fkey FOREIGN KEY (hearth_user_id) REFERENCES public.hearth_user(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: hearth_user_info hearth_user_info_hearth_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_info
    ADD CONSTRAINT hearth_user_info_hearth_user_id_fkey FOREIGN KEY (hearth_user_id) REFERENCES public.hearth_user(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: hearth_user_links hearth_user_links_child_hearth_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_links
    ADD CONSTRAINT hearth_user_links_child_hearth_id_fkey FOREIGN KEY (child_hearth_id) REFERENCES public."User"(id) ON DELETE CASCADE;


--
-- Name: hearth_user_links hearth_user_links_link_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_links
    ADD CONSTRAINT hearth_user_links_link_id_fkey FOREIGN KEY (link_id) REFERENCES public.email_links(id) ON DELETE CASCADE;


--
-- Name: hearth_user_links hearth_user_links_primary_hearth_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_links
    ADD CONSTRAINT hearth_user_links_primary_hearth_id_fkey FOREIGN KEY (primary_hearth_id) REFERENCES public."User"(id) ON DELETE CASCADE;


--
-- Name: hearth_user_voice hearth_user_voice_hearth_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_voice
    ADD CONSTRAINT hearth_user_voice_hearth_user_id_fkey FOREIGN KEY (hearth_user_id) REFERENCES public.hearth_user(id) ON DELETE CASCADE;


--
-- Name: hearth_user_voice hearth_user_voice_voice_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hearth_user_voice
    ADD CONSTRAINT hearth_user_voice_voice_id_fkey FOREIGN KEY (voice_id) REFERENCES public.voice(id) ON DELETE CASCADE;


--
-- Name: import_entity import_entity_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.import_entity
    ADD CONSTRAINT import_entity_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON DELETE SET NULL;


--
-- Name: in_progress_email_link in_progress_email_link_hearth_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.in_progress_email_link
    ADD CONSTRAINT in_progress_email_link_hearth_user_id_fkey FOREIGN KEY (hearth_user_id) REFERENCES public.hearth_user(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: in_progress_user_link in_progress_user_link_hearth_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.in_progress_user_link
    ADD CONSTRAINT in_progress_user_link_hearth_user_id_fkey FOREIGN KEY (hearth_user_id) REFERENCES public.hearth_user(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: mcp_servers mcp_servers_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.mcp_servers
    ADD CONSTRAINT mcp_servers_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."User"(id) ON DELETE CASCADE;


--
-- Name: processing_ai_projections processing_ai_projections_ai_projection_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.processing_ai_projections
    ADD CONSTRAINT processing_ai_projections_ai_projection_id_fkey FOREIGN KEY (ai_projection_id) REFERENCES public.ai_projection(id) ON DELETE CASCADE;


--
-- Name: promoted_shared_mailboxes promoted_shared_mailboxes_hearth_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promoted_shared_mailboxes
    ADD CONSTRAINT promoted_shared_mailboxes_hearth_id_fkey FOREIGN KEY (hearth_id) REFERENCES public."User"(id) ON DELETE CASCADE;


--
-- Name: property_definitions property_definitions_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.property_definitions
    ADD CONSTRAINT property_definitions_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON DELETE CASCADE;


--
-- Name: property_definitions property_definitions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.property_definitions
    ADD CONSTRAINT property_definitions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."User"(id) ON DELETE SET NULL;


--
-- Name: property_options property_options_property_definition_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.property_options
    ADD CONSTRAINT property_options_property_definition_id_fkey FOREIGN KEY (property_definition_id) REFERENCES public.property_definitions(id) ON DELETE CASCADE;


--
-- Name: resolved_message_content resolved_message_content_messageId_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolved_message_content
    ADD CONSTRAINT "resolved_message_content_messageId_fkey" FOREIGN KEY ("messageId") REFERENCES public."ChatMessage"(id) ON DELETE CASCADE;


--
-- Name: saved_view saved_view_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saved_view
    ADD CONSTRAINT saved_view_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: scheduled_action scheduled_action_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scheduled_action
    ADD CONSTRAINT scheduled_action_owner_fkey FOREIGN KEY (owner) REFERENCES public."User"(id) ON DELETE CASCADE;


--
-- Name: task_duplicate_embedding task_duplicate_embedding_document_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_duplicate_embedding
    ADD CONSTRAINT task_duplicate_embedding_document_id_fkey FOREIGN KEY (document_id) REFERENCES public."Document"(id) ON DELETE CASCADE;


--
-- Name: task_duplicate_match task_duplicate_match_duplicate_task_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_duplicate_match
    ADD CONSTRAINT task_duplicate_match_duplicate_task_id_fkey FOREIGN KEY (duplicate_task_id) REFERENCES public."Document"(id) ON DELETE CASCADE;


--
-- Name: task_duplicate_match task_duplicate_match_task_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_duplicate_match
    ADD CONSTRAINT task_duplicate_match_task_id_fkey FOREIGN KEY (task_id) REFERENCES public."Document"(id) ON DELETE CASCADE;


--
-- Name: team_crm_settings team_crm_settings_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_crm_settings
    ADD CONSTRAINT team_crm_settings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON DELETE CASCADE;


--
-- Name: team_invite team_invite_invited_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_invite
    ADD CONSTRAINT team_invite_invited_by_fkey FOREIGN KEY (invited_by) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: team_invite team_invite_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_invite
    ADD CONSTRAINT team_invite_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: team team_owner_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team
    ADD CONSTRAINT team_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: team_task_counter team_task_counter_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_task_counter
    ADD CONSTRAINT team_task_counter_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON DELETE CASCADE;


--
-- Name: team_task team_task_document_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_task
    ADD CONSTRAINT team_task_document_id_fkey FOREIGN KEY (document_id) REFERENCES public."Document"(id) ON DELETE CASCADE;


--
-- Name: team_task team_task_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_task
    ADD CONSTRAINT team_task_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON DELETE CASCADE;


--
-- Name: team_user team_user_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_user
    ADD CONSTRAINT team_user_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.team(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: team_user team_user_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_user
    ADD CONSTRAINT team_user_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."User"(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: user_ai_projection user_ai_projection_ai_projection_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_ai_projection
    ADD CONSTRAINT user_ai_projection_ai_projection_id_fkey FOREIGN KEY (ai_projection_id) REFERENCES public.ai_projection(id) ON DELETE CASCADE;


--
-- Name: user_notification user_notification_notification_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_notification
    ADD CONSTRAINT user_notification_notification_id_fkey FOREIGN KEY (notification_id) REFERENCES public.notification(id) ON DELETE CASCADE;


--
-- Name: webhook_delivery_attempt webhook_delivery_attempt_webhook_delivery_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_delivery_attempt
    ADD CONSTRAINT webhook_delivery_attempt_webhook_delivery_id_fkey FOREIGN KEY (webhook_delivery_id) REFERENCES public.webhook_delivery(id);


--
-- Name: webhook_delivery webhook_delivery_webhook_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_delivery
    ADD CONSTRAINT webhook_delivery_webhook_id_fkey FOREIGN KEY (webhook_id) REFERENCES public.webhook(id);


--
-- PostgreSQL database dump complete
--

\unrestrict ptgM2OhvmH1jtNx6q2c4i2KHKKl6vhw2G3NEleLNxpX34Diq8NE2ZOBn0YVFlju

