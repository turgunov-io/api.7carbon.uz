-- carbon_go bootstrap schema
-- Run this file once in a clean DB (or repeatedly; all CREATE statements are idempotent).
-- Active API routes in main.go:
-- /healthz
-- /contact
-- /about
-- /banners
-- /partners
-- /tuning
-- /accessories
-- /service_offerings
-- /privacy_sections
-- /api/consultations
-- /portfolio_items
-- /work_post

BEGIN;

-- Optional but explicit.
CREATE SCHEMA IF NOT EXISTS public;

-- 2. Banners (used by GET /banners)
CREATE TABLE IF NOT EXISTS public.banners (
    id SERIAL PRIMARY KEY,
    section TEXT NOT NULL,
    title TEXT NOT NULL,
    image_url TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0
);

-- 3.1 Service cards/details (type -> title -> detailed page content)
CREATE TABLE IF NOT EXISTS public.service_offerings (
    id SERIAL PRIMARY KEY,
    service_type TEXT NOT NULL,
    title TEXT NOT NULL,
    detailed_description TEXT,
    gallery_images JSONB NOT NULL DEFAULT '[]'::jsonb,
    price_text TEXT,
    position INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT service_offerings_gallery_images_is_array_chk
        CHECK (jsonb_typeof(gallery_images) = 'array')
);

-- 5. Portfolio (used by GET /portfolio_items)
CREATE TABLE IF NOT EXISTS public.portfolio_items (
    id SERIAL PRIMARY KEY,
    brand TEXT,
    title TEXT NOT NULL,
    image_url TEXT NOT NULL,
    description TEXT,
    youtube_link TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 6. Partners
CREATE TABLE IF NOT EXISTS public.partners (
    id SERIAL PRIMARY KEY,
    name TEXT,
    logo_url TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0
);

-- 8. Tuning page
CREATE TABLE IF NOT EXISTS public.tuning_cards (
    id SERIAL PRIMARY KEY,
    title TEXT NOT NULL,
    image_url TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0
);

-- 8.1 Tuning posts/cards
CREATE TABLE IF NOT EXISTS public.tuning (
    id SERIAL PRIMARY KEY,
    brand TEXT,
    model TEXT,
    card_image_url TEXT,
    full_image_url JSONB NOT NULL DEFAULT '[]'::jsonb,
    price TEXT,
    description TEXT,
    card_description TEXT,
    full_description TEXT,
    video_image_url TEXT,
    video_link TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Ensure compatibility for already existing databases.
-- This block safely creates/converts full_image_url to JSONB array.
DO $$
DECLARE col_type TEXT;
BEGIN
    IF to_regclass('public.tuning') IS NULL THEN
        RETURN;
    END IF;

    SELECT data_type
      INTO col_type
      FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'tuning'
       AND column_name = 'full_image_url';

    IF col_type IS NULL THEN
        ALTER TABLE public.tuning
            ADD COLUMN full_image_url JSONB NOT NULL DEFAULT '[]'::jsonb;
    ELSIF col_type <> 'jsonb' THEN
        ALTER TABLE public.tuning
            ALTER COLUMN full_image_url TYPE JSONB
            USING CASE
                WHEN full_image_url IS NULL OR btrim(full_image_url::text) = '' THEN '[]'::jsonb
                WHEN left(btrim(full_image_url::text), 1) = '[' THEN full_image_url::jsonb
                ELSE jsonb_build_array(full_image_url)
            END;
        ALTER TABLE public.tuning
            ALTER COLUMN full_image_url SET DEFAULT '[]'::jsonb;
        UPDATE public.tuning
           SET full_image_url = '[]'::jsonb
         WHERE full_image_url IS NULL;
        ALTER TABLE public.tuning
            ALTER COLUMN full_image_url SET NOT NULL;
    ELSE
        ALTER TABLE public.tuning
            ALTER COLUMN full_image_url SET DEFAULT '[]'::jsonb;
        UPDATE public.tuning
           SET full_image_url = '[]'::jsonb
         WHERE full_image_url IS NULL;
        ALTER TABLE public.tuning
            ALTER COLUMN full_image_url SET NOT NULL;
    END IF;

    ALTER TABLE public.tuning
        DROP CONSTRAINT IF EXISTS tuning_full_image_url_is_array_chk;

    ALTER TABLE public.tuning
        ADD CONSTRAINT tuning_full_image_url_is_array_chk
        CHECK (jsonb_typeof(full_image_url) = 'array');
END $$;

ALTER TABLE IF EXISTS public.tuning
    ADD COLUMN IF NOT EXISTS price TEXT;

ALTER TABLE IF EXISTS public.tuning
    ADD COLUMN IF NOT EXISTS brand TEXT;

ALTER TABLE IF EXISTS public.tuning
    ADD COLUMN IF NOT EXISTS model TEXT;

-- 8.2 Accessories posts/cards
CREATE TABLE IF NOT EXISTS public.accessories (
    id SERIAL PRIMARY KEY,
    title TEXT NOT NULL,
    card_image_url TEXT,
    full_image_url JSONB NOT NULL DEFAULT '[]'::jsonb,
    price TEXT,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Ensure compatibility for already existing databases.
-- This block safely creates/converts full_image_url to JSONB array.
DO $$
DECLARE col_type TEXT;
BEGIN
    IF to_regclass('public.accessories') IS NULL THEN
        RETURN;
    END IF;

    ALTER TABLE public.accessories
        ADD COLUMN IF NOT EXISTS title TEXT;

    UPDATE public.accessories
       SET title = 'Accessory'
     WHERE title IS NULL OR btrim(title) = '';

    ALTER TABLE public.accessories
        ALTER COLUMN title SET NOT NULL;

    ALTER TABLE public.accessories
        ADD COLUMN IF NOT EXISTS card_image_url TEXT;

    ALTER TABLE public.accessories
        ADD COLUMN IF NOT EXISTS price TEXT;

    ALTER TABLE public.accessories
        ADD COLUMN IF NOT EXISTS description TEXT;

    ALTER TABLE public.accessories
        ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

    ALTER TABLE public.accessories
        ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

    SELECT data_type
      INTO col_type
      FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'accessories'
       AND column_name = 'full_image_url';

    IF col_type IS NULL THEN
        ALTER TABLE public.accessories
            ADD COLUMN full_image_url JSONB NOT NULL DEFAULT '[]'::jsonb;
    ELSIF col_type <> 'jsonb' THEN
        ALTER TABLE public.accessories
            ALTER COLUMN full_image_url TYPE JSONB
            USING CASE
                WHEN full_image_url IS NULL OR btrim(full_image_url::text) = '' THEN '[]'::jsonb
                WHEN left(btrim(full_image_url::text), 1) = '[' THEN full_image_url::jsonb
                ELSE jsonb_build_array(full_image_url)
            END;
        ALTER TABLE public.accessories
            ALTER COLUMN full_image_url SET DEFAULT '[]'::jsonb;
        UPDATE public.accessories
           SET full_image_url = '[]'::jsonb
         WHERE full_image_url IS NULL;
        ALTER TABLE public.accessories
            ALTER COLUMN full_image_url SET NOT NULL;
    ELSE
        ALTER TABLE public.accessories
            ALTER COLUMN full_image_url SET DEFAULT '[]'::jsonb;
        UPDATE public.accessories
           SET full_image_url = '[]'::jsonb
         WHERE full_image_url IS NULL;
        ALTER TABLE public.accessories
            ALTER COLUMN full_image_url SET NOT NULL;
    END IF;

    ALTER TABLE public.accessories
        DROP CONSTRAINT IF EXISTS accessories_full_image_url_is_array_chk;

    ALTER TABLE public.accessories
        ADD CONSTRAINT accessories_full_image_url_is_array_chk
        CHECK (jsonb_typeof(full_image_url) = 'array');
END $$;

-- 9. About page
CREATE TABLE IF NOT EXISTS public.about_page (
    id SMALLINT PRIMARY KEY DEFAULT 1,
    banner_image_url TEXT,
    banner_title TEXT,
    history_description TEXT,
    video_url TEXT,
    mission_description TEXT,
    mission_image_url TEXT
);

CREATE TABLE IF NOT EXISTS public.about_metrics (
    id SERIAL PRIMARY KEY,
    about_id SMALLINT NOT NULL DEFAULT 1 REFERENCES public.about_page(id) ON DELETE CASCADE,
    metric_key TEXT NOT NULL,
    metric_value TEXT NOT NULL,
    metric_label TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    UNIQUE (about_id, metric_key)
);

CREATE TABLE IF NOT EXISTS public.about_sections (
    id SERIAL PRIMARY KEY,
    about_id SMALLINT NOT NULL DEFAULT 1 REFERENCES public.about_page(id) ON DELETE CASCADE,
    section_key TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    UNIQUE (about_id, section_key)
);

-- 10. Contact page
CREATE TABLE IF NOT EXISTS public.contact_page (
    id SMALLINT PRIMARY KEY DEFAULT 1,
    phone_number TEXT,
    address TEXT,
    description TEXT,
    image_url TEXT
);

-- 10.1 Contact (used by GET /contact when table exists)
CREATE TABLE IF NOT EXISTS public.contact (
    id SERIAL PRIMARY KEY,
    phone_number TEXT,
    address TEXT,
    description TEXT,
    email TEXT,
    work_schedule TEXT
);

-- 11.1 Mobile app consultations
CREATE TABLE IF NOT EXISTS public.consultations (
    id BIGSERIAL PRIMARY KEY,
    first_name TEXT NOT NULL,
    last_name TEXT NOT NULL,
    phone TEXT NOT NULL,
    service_type TEXT NOT NULL,
    car_model TEXT,
    preferred_call_time TEXT,
    comments TEXT,
    status TEXT NOT NULL DEFAULT 'new'
        CHECK (status IN ('new', 'in_progress', 'completed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 12. Privacy policy
CREATE TABLE IF NOT EXISTS public.privacy_sections (
    id SERIAL PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0
);

-- 15. Work posts (used by GET /work_post)
-- Backend first tries public.work_post, then falls back to public.blog_posts.
CREATE TABLE IF NOT EXISTS public.work_post (
    id SERIAL PRIMARY KEY,
    title_model TEXT NOT NULL,
    card_image_url TEXT,
    full_image_url TEXT,
    card_description TEXT,
    work_list JSONB,
    gallery_images JSONB,
    full_description TEXT,
    video_image_url TEXT,
    video_link TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Legacy compatibility table.
CREATE TABLE IF NOT EXISTS public.blog_posts (
    id SERIAL PRIMARY KEY,
    title_model TEXT NOT NULL,
    card_image_url TEXT,
    full_image_url TEXT,
    card_description TEXT,
    work_list JSONB,
    gallery_images JSONB,
    full_description TEXT,
    video_image_url TEXT,
    video_link TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Ensure compatibility for already existing databases.
ALTER TABLE IF EXISTS public.work_post
    ADD COLUMN IF NOT EXISTS gallery_images JSONB;

ALTER TABLE IF EXISTS public.blog_posts
    ADD COLUMN IF NOT EXISTS gallery_images JSONB;

-- Indexes for active API sort patterns.
CREATE INDEX IF NOT EXISTS idx_banners_priority_id
    ON public.banners (priority, id);

CREATE INDEX IF NOT EXISTS idx_portfolio_items_created_at_id
    ON public.portfolio_items (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_work_post_created_at_id
    ON public.work_post (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_blog_posts_created_at_id
    ON public.blog_posts (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_tuning_created_at_id
    ON public.tuning (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_accessories_created_at_id
    ON public.accessories (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_service_offerings_type_position
    ON public.service_offerings (service_type, position, id);

CREATE INDEX IF NOT EXISTS idx_about_metrics_about_position
    ON public.about_metrics (about_id, position, id);

CREATE INDEX IF NOT EXISTS idx_about_sections_about_position
    ON public.about_sections (about_id, position, id);

CREATE INDEX IF NOT EXISTS idx_consultations_created_at_id
    ON public.consultations (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_consultations_status_created_at
    ON public.consultations (status, created_at DESC, id DESC);

-- Seed data for active routes (insert only when table is empty).
INSERT INTO public.banners (section, title, image_url, priority)
SELECT 'home', 'Main banner', 'https://example.com/banner-1.jpg', 1
WHERE NOT EXISTS (SELECT 1 FROM public.banners);

INSERT INTO public.portfolio_items (brand, title, image_url, description, youtube_link)
SELECT
    'BMW',
    'Demo portfolio item',
    'https://example.com/portfolio-1.jpg',
    'Demo description',
    NULL
WHERE NOT EXISTS (SELECT 1 FROM public.portfolio_items);

INSERT INTO public.work_post (
    title_model,
    card_image_url,
    full_image_url,
    card_description,
    work_list,
    gallery_images,
    full_description,
    video_image_url,
    video_link
)
SELECT
    'Tesla Model 3',
    'https://example.com/work-card.jpg',
    'https://example.com/work-full.jpg',
    'Power and throttle response upgrade',
    '[{"step":"Diagnostics"},{"step":"Calibration"},{"step":"Road test"}]'::jsonb,
    '["https://example.com/work-1.jpg","https://example.com/work-2.jpg","https://example.com/work-3.jpg"]'::jsonb,
    'Stage 1 tuning with stable daily setup.',
    'https://example.com/work-video-cover.jpg',
    'https://www.youtube.com/watch?v=dQw4w9WgXcQ'
WHERE NOT EXISTS (SELECT 1 FROM public.work_post);

-- Seed data for /about.
INSERT INTO public.about_page (
    id,
    banner_title,
    history_description,
    mission_description
)
SELECT
    1,
    'О КОМПАНИИ',
    'В 7 Carbon мы идем дальше, предлагая нашим клиентам нечто более чем стандартные решения. Мы разрабатываем и изготавливаем детали из углеволокна под заказ, чтобы ваш автомобиль стал уникальным произведением искусства.',
    'Наша команда дизайнеров и инженеров работает с вами, чтобы воплотить в жизнь вашу уникальную визию и создать автомобиль, который подчеркнет ваш стиль и индивидуальность.'
WHERE NOT EXISTS (SELECT 1 FROM public.about_page WHERE id = 1);

INSERT INTO public.about_metrics (about_id, metric_key, metric_value, metric_label, position)
SELECT
    1,
    src.metric_key,
    src.metric_value,
    src.metric_label,
    src.position
FROM (
    VALUES
        ('client_projects', '20+', 'Клиентских проектов', 1),
        ('manufactured_parts', '7500+', 'Изготовленных деталей', 2),
        ('key_partners', '11', 'Крупных партнёров', 3)
) AS src(metric_key, metric_value, metric_label, position)
WHERE NOT EXISTS (SELECT 1 FROM public.about_metrics WHERE about_id = 1);

INSERT INTO public.about_sections (about_id, section_key, title, description, position)
SELECT
    1,
    src.section_key,
    src.title,
    src.description,
    src.position
FROM (
    VALUES
        (
            'history',
            'КРАТКАЯ ИСТОРИЯ 7 CARBON',
            'Зарождение 7 Carbon в 2020 году было исключительно страстью к автомобилям, выросшей в профессиональное тюнинг ателье. С момента своего создания мы стремились преобразовывать автомобили в уникальные произведения искусства с инновационным дизайном. Начав с небольших гаражей, мы выросли в узнаваемый бренд, поистине цененный в мире автотюнинга.

7 Carbon стало не просто именем, а философией, где техническое мастерство сочетается с творческим вдохновением. Каждый наш проект - это уникальное творение, отражающее индивидуальность и стиль. Сегодня 7 Carbon - это история страсти и инноваций, оставляющая свой неповторимый след в автомобильной индустрии.',
            1
        ),
        (
            'philosophy',
            'ФИЛОСОФИЯ',
            'Принципы, которыми руководствуется 7 Carbon, определяют наше место в мире тюнинга. Наш подход основан на тщательном балансе между техническим мастерством и творчеством. Мы стремимся к совершенству в каждом проекте, выделяясь инновационными решениями и качественной реализацией.

Наша команда избегает шаблонов, придерживаясь философии индивидуализации. Мы уважаем искусство автомобильного дизайна, поэтому каждый проект - это уникальная история, рассказанная через детали и формы. Технологический прогресс и инновации - в основе нашей работы.',
            2
        ),
        (
            'certification',
            'СЕРТИФИКАЦИЯ',
            'В 7 Carbon мы придаем первостепенное значение качеству, сертификации и профессионализму. Каждый материал, использованный в наших проектах, проходит строгий отбор и сертификацию, гарантируя высший стандарт. Наша команда специалистов также подвергается сертификации, обеспечивая мастерство и навыки на высочайшем уровне.

Мы сотрудничаем только с проверенными поставщиками, чтобы предоставлять клиентам материалы выдающегося качества. Этот подход обеспечивает долговечность и надежность каждого элемента, воплощенного в наших творениях.',
            3
        )
) AS src(section_key, title, description, position)
WHERE NOT EXISTS (SELECT 1 FROM public.about_sections WHERE about_id = 1);

COMMIT;

-- Optional checks after execution:
-- SELECT COUNT(*) FROM public.contact;
-- SELECT COUNT(*) FROM public.about_page;
-- SELECT COUNT(*) FROM public.banners;
-- SELECT COUNT(*) FROM public.partners;
-- SELECT COUNT(*) FROM public.tuning;
-- SELECT COUNT(*) FROM public.accessories;
-- SELECT COUNT(*) FROM public.service_offerings;
-- SELECT COUNT(*) FROM public.privacy_sections;
-- SELECT COUNT(*) FROM public.consultations;
-- SELECT COUNT(*) FROM public.portfolio_items;
-- SELECT COUNT(*) FROM public.work_post;
-- SELECT COUNT(*) FROM public.about_metrics;
-- SELECT COUNT(*) FROM public.about_sections;



-- Файл: schema.sql (line 1)

-- Запуск для новой БД:

-- psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f schema.sql
-- Быстрая проверка после запуска:

-- SELECT COUNT(*) FROM public.about_page;
-- SELECT COUNT(*) FROM public.about_metrics;
-- SELECT COUNT(*) FROM public.about_secti
