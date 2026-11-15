// Site-wide internationalization dictionary and translation helpers.
export type Locale = "zh" | "en";

export interface TranslationDict {
  nav: {
    featured: string;
    hot: string;
    daily: string;
    model: string;
    interests: string;
    sources: string;
    history: string;
    about: string;
    account: string;
    workspace: string;
    historyAndAbout: string;
    github: string;
  };
  shell: {
    skipToContent: string;
    backToTop: string;
    appearance: string;
    language: string;
  };
  action: {
    backToHot: string;
    backToFeed: string;
    readOriginal: string;
    openOriginal: string;
    logout: string;
    login: string;
    register: string;
    save: string;
    saving: string;
    testConnection: string;
    testing: string;
    viewTestResult: string;
    pleaseWait: string;
    deleteAccount: string;
    permanentlyDelete: string;
    deleting: string;
    confirmUnderstand: string;
    enterPasswordConfirm: string;
    translate: string;
    showOriginal: string;
    translating: string;
    translated: string;
    bilingual: string;
  };
  article: {
    aiBrief: string;
    originalMedia: string;
    mediaNotice: string;
    recommendReason: string;
    score: string;
    source: string;
    publishTime: string;
    matched: string;
    last48Hours: string;
    hotRankTitle: string;
    hotRankSubtitle: string;
    noHotspots: string;
    noResultsYet: string;
  };
  model: {
    title: string;
    subtitle: string;
    baseUrl: string;
    modelName: string;
    apiKey: string;
    apiKeySavedHint: string;
    apiKeyPlaceholder: string;
    notice: string;
    savedSuccess: string;
  };
  account: {
    title: string;
    terms: string;
    privacy: string;
    deleteNotice: string;
  };
}

export const TRANSLATIONS: Record<Locale, TranslationDict> = {
  zh: {
    nav: {
      featured: "我的热点",
      hot: "个人热点榜",
      daily: "我的日报",
      model: "模型配置",
      interests: "关注项",
      sources: "我的信源",
      history: "历史归档",
      about: "关于 MyHOT",
      account: "我的账号",
      workspace: "个人工作区",
      historyAndAbout: "历史与关于",
      github: "GitHub 开源",
    },
    shell: {
      skipToContent: "跳到正文",
      backToTop: "回到顶部",
      appearance: "外观",
      language: "语言",
    },
    action: {
      backToHot: "← 返回热点榜",
      backToFeed: "← 我的热点",
      readOriginal: "阅读原文 ↗",
      openOriginal: "打开原文 ↗",
      logout: "退出登录",
      login: "登录",
      register: "注册",
      save: "保存",
      saving: "正在保存…",
      testConnection: "测试连接",
      testing: "连接测试已排队…",
      viewTestResult: "查看测试结果",
      pleaseWait: "请稍候…",
      deleteAccount: "删除账号",
      permanentlyDelete: "永久删除账号",
      deleting: "正在删除…",
      confirmUnderstand: "我理解此操作会立即删除个人数据",
      enterPasswordConfirm: "输入当前密码确认",
      translate: "翻译为英文",
      showOriginal: "显示中文摘要",
      translating: "正在翻译…",
      translated: "英文译文",
      bilingual: "双语对照",
    },
    article: {
      aiBrief: "AI 导读",
      originalMedia: "原文附图",
      mediaNotice: "附图按原文提取，阅读上下文请打开原文 ↗",
      recommendReason: "推荐理由",
      score: "AI 评分",
      source: "来源",
      publishTime: "发布时间",
      matched: "匹配",
      last48Hours: "LAST 48 HOURS",
      hotRankTitle: "个人热点榜",
      hotRankSubtitle: "按你的模型评分排序，最多展示 10 条。",
      noHotspots: "过去 48 小时还没有匹配到的文章。",
      noResultsYet: "暂时还没有个人结果。新增文章命中你的关注项后会显示在这里。",
    },
    model: {
      title: "模型配置",
      subtitle: "填写任意兼容 OpenAI Chat Completions 的公网 HTTPS 接口。MyHOT 会使用此配置生成你的摘要和日报。",
      baseUrl: "Base URL",
      modelName: "模型名称",
      apiKey: "API Key",
      apiKeySavedHint: "（已保存；留空保持不变）",
      apiKeyPlaceholder: "粘贴 API Key",
      notice: "API Key 加密保存。文章内容会发送到你配置的模型服务商，由该服务商按你的 API Key 计费；测试连接会发送一次请求。",
      savedSuccess: "模型配置已保存，只会用于之后命中的新文章。",
    },
    account: {
      title: "我的账号",
      terms: "使用规则",
      privacy: "隐私说明",
      deleteNotice: "删除后会清除你的模型设置、关注项、会话、个人摘要和日报；共享信源与文章继续保留。",
    },
  },
  en: {
    nav: {
      featured: "My Feed",
      hot: "Hotspots",
      daily: "Daily Brief",
      model: "Model Settings",
      interests: "Interests",
      sources: "My Sources",
      history: "Archive",
      about: "About MyHOT",
      account: "My Account",
      workspace: "Workspace",
      historyAndAbout: "History & About",
      github: "GitHub",
    },
    shell: {
      skipToContent: "Skip to content",
      backToTop: "Back to top",
      appearance: "Appearance",
      language: "Language",
    },
    action: {
      backToHot: "← Back to Hotlist",
      backToFeed: "← Back to Feed",
      readOriginal: "Read Original ↗",
      openOriginal: "Open Original ↗",
      logout: "Log out",
      login: "Log in",
      register: "Register",
      save: "Save",
      saving: "Saving…",
      testConnection: "Test Connection",
      testing: "Test queued…",
      viewTestResult: "View Result",
      pleaseWait: "Please wait…",
      deleteAccount: "Delete Account",
      permanentlyDelete: "Permanently Delete",
      deleting: "Deleting…",
      confirmUnderstand: "I understand this permanently deletes personal data",
      enterPasswordConfirm: "Enter current password to confirm",
      translate: "Translate to English",
      showOriginal: "Show Original Summary",
      translating: "Translating…",
      translated: "English Translation",
      bilingual: "Bilingual",
    },
    article: {
      aiBrief: "AI Brief",
      originalMedia: "Original Media",
      mediaNotice: "Media extracted from source. Open original for complete context ↗",
      recommendReason: "Recommendation",
      score: "AI Score",
      source: "Source",
      publishTime: "Published",
      matched: "Matched",
      last48Hours: "LAST 48 HOURS",
      hotRankTitle: "Personal Hotspots",
      hotRankSubtitle: "Ranked by your model score, showing top 10 articles.",
      noHotspots: "No matching articles found in the last 48 hours.",
      noResultsYet: "No personal results yet. New articles matching your interests will appear here.",
    },
    model: {
      title: "Model Settings",
      subtitle: "Configure any OpenAI Chat Completions compatible HTTPS endpoint. MyHOT uses this to generate summaries and briefs.",
      baseUrl: "Base URL",
      modelName: "Model Name",
      apiKey: "API Key",
      apiKeySavedHint: "(Saved; leave blank to keep unchanged)",
      apiKeyPlaceholder: "Paste API Key",
      notice: "API Key is encrypted. Article text is sent directly to your model provider and billed to your key; testing connection sends one request.",
      savedSuccess: "Model configuration saved. It will apply to future matching articles.",
    },
    account: {
      title: "My Account",
      terms: "Terms of Use",
      privacy: "Privacy Policy",
      deleteNotice: "Deletion permanently removes your model settings, interests, sessions, summaries, and briefs. Shared sources and articles are retained.",
    },
  },
};

/** Get the translations dictionary for a given locale. */
export function getDict(locale: Locale): TranslationDict {
  return TRANSLATIONS[locale] ?? TRANSLATIONS.zh;
}
