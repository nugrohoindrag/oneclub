import React, { useState, useEffect, useRef } from 'react';
import { AnimatePresence } from 'motion/react';
import {
  // 01. Foundations & Tokens
  Icon,
  // 02. Layout
  Container,
  Stack,
  Row,
  Grid,
  SplitLayout,
  AppShell,
  Section,
  // 03. Navigation
  TopAppBar,
  BottomAppBar,
  NavigationBar,
  NavigationRail,
  NavigationDrawer,
  Breadcrumbs,
  Tabs,
  Pagination,
  // 04. Actions
  Button,
  IconButton,
  FAB,
  ButtonGroup,
  SplitButton,
  Menu,
  // 05. Inputs & Forms
  FilledTextField,
  OutlinedTextField,
  Select,
  MultiSelect,
  DatePickerModal,
  DatePickerInline,
  DateRangePickerModal,
  TimePickerModal,
  Checkbox,
  Radio,
  Switch,
  Slider,
  TextArea,
  NumberInput,
  // 06. Selection
  Chip,
  SegmentedButton,
  ToggleGroup,
  FilterBar,
  // 07. Surfaces & Containers
  Card,
  HeroCard,
  SectionCard,
  Panel,
  Surface,
  BottomSheet,
  SideSheet,
  Carousel,
  ListItem,
  Divider,
  // 08. Data Display
  Stat,
  StatusBadge,
  Avatar,
  AvatarGroup,
  Timeline,
  AdvancedDataTable,
  InsightCard,
  StatusCard,
  MetricCard,
  Badge,
  // 09. Data Visualization
  ChartCard,
  LineChart,
  AreaChart,
  BarChart,
  DonutChart,
  GaugeChart,
  Sparkline,
  ChartLegend,
  ChartControls,
  HorizontalBarChart,
  StackedBarChart,
  PieChart,
  RadarChart,
  HeatmapGrid,
  // 10. Feedback & Status
  AlertCallout,
  InfoBanner,
  WarningBanner,
  Snackbar,
  Toast,
  LinearProgress,
  CircularProgress,
  LoadingSpinner,
  Skeleton,
  SkeletonKPI,
  SkeletonChart,
  SkeletonTable,
  EmptyState,
  ErrorState,
  SuccessState,
  // 11. Overlays
  Dialog,
  Modal,
  Popover,
  ContextMenu,
  Tooltip,
  // 12. Utility
  SearchBar,
  DateFilter,
  ExportButton,
  RefreshButton,
  ViewSwitcher,
  SortControl,
  // 14. Patterns
  MapCard,
  AlertSummary,
  StatusOverview,
  // 15. Templates
  ExecutiveDashboard,
  EcommerceStorefront,
  SaasCrmWorkspace,
  ProductItem,
  // Motion engine
  MorphicMotionConfig,
  Presence,
  PresenceList,
  SharedIndicator,
  Reveal,
  StaggerReveal,
  Parallax,
  ScrollProgressBar,
  ScrollScale,
  SplitText,
  ScrambleText,
  CountUp,
  DrawSVG,
  AnimatedCircularProgress,
  Magnetic,
  Pressable,
  Tilt,
  MotionScrubber,
  ParticleBurst,
  LayoutTransformDemo,
  SvgDrawMotion,
  MagneticMotion,
  StaggerList,
  SpringPlayground,
  M3_TRANSITIONS,
  M3_SPRING,
  animate,
  motion,
  LayoutGroup,
  // Design tokens
  THEME_ACCENT_META,
  type ThemeAccent,
  type ThemeMode,
} from '../index.js';

// Examples are published from a separate entry point so Core stays
// domain-neutral. A real application imports only what it needs.
import { WarehouseCard, DeliveryCard } from '../examples/index.js';

export default function App() {
  const [theme, setTheme] = useState<ThemeMode>('dark');
  const [accent, setAccent] = useState<ThemeAccent>('lime');
  const [activeCategory, setActiveCategory] = useState<string>('01_foundations');

  // Interactive component states
  const [segmentedVal, setSegmentedVal] = useState('daily');
  const [switchChecked, setSwitchChecked] = useState(true);
  const [checkboxChecked, setCheckboxChecked] = useState(true);
  const [radioSelected, setRadioSelected] = useState('option1');
  const [sliderVal, setSliderVal] = useState(65);
  const [tabActive, setTabActive] = useState('tab1');
  const [searchValue, setSearchValue] = useState('');
  const [paginationPage, setPaginationPage] = useState(1);
  const [activeFilterId, setActiveFilterId] = useState('all');
  const [currentViewMode, setCurrentViewMode] = useState<'grid' | 'list' | 'table'>('grid');
  const [sortField, setSortField] = useState('name');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('asc');
  const [activeDatePeriod, setActiveDatePeriod] = useState('7d');
  const [refreshLoading, setRefreshLoading] = useState(false);

  // Motion interactive states distributed across categories
  const [transitionKey, setTransitionKey] = useState(0);
  const [springToggled, setSpringToggled] = useState(false);
  const [layoutTabActive, setLayoutTabActive] = useState('Overview');
  const [textSeed, setTextSeed] = useState(0);
  const [svgSeed, setSvgSeed] = useState(0);
  const [svgProgress, setSvgProgress] = useState(72);
  const [presenceOpen, setPresenceOpen] = useState(true);
  const [presenceRows, setPresenceRows] = useState([
    { id: 1, label: 'Payment received' },
    { id: 2, label: 'Invoice issued' },
    { id: 3, label: 'Refund processed' },
  ]);
  const presenceNext = useRef(4);
  const scrubberBoxRef = useRef<HTMLDivElement>(null);
  const [scrubberControls, setScrubberControls] = useState<ReturnType<typeof animate> | null>(null);

  useEffect(() => {
    if (!scrubberBoxRef.current) return;
    const playback = animate(
      scrubberBoxRef.current,
      { x: [0, 180, 0], rotate: [0, 180, 360] },
      { duration: 3, repeat: Infinity, ease: 'linear' },
    );
    setScrubberControls(playback);
    return () => playback.stop();
  }, [activeCategory]);

  // Advanced Inputs State
  const [selectedCategoryValue, setSelectedCategoryValue] = useState('tech');
  const [selectedTags, setSelectedTags] = useState<string[]>(['react', 'design_system']);
  const [textComment, setTextComment] = useState('Morphic Design System provides the most comprehensive enterprise component library.');
  const [nodeCount, setNodeCount] = useState(12);
  const [toggleGroupVal, setToggleGroupVal] = useState('grid');

  // Modals & Sheets
  const [isDialogOpen, setIsDialogOpen] = useState(false);
  const [isGenericModalOpen, setIsGenericModalOpen] = useState(false);
  const [isSheetOpen, setIsSheetOpen] = useState(false);
  const [isSideSheetOpen, setIsSideSheetOpen] = useState(false);
  const [isSnackbarOpen, setIsSnackbarOpen] = useState(false);
  const [isDatePickerOpen, setIsDatePickerOpen] = useState(false);
  const [isDateRangePickerOpen, setIsDateRangePickerOpen] = useState(false);
  const [isTimePickerOpen, setIsTimePickerOpen] = useState(false);
  const [toasts, setToasts] = useState<{ id: string; title: string; message: string; type: 'success' | 'info' | 'warning' | 'error' }[]>([]);

  const [isMobileNavOpen, setIsMobileNavOpen] = useState(false);

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme);
    document.documentElement.setAttribute('data-accent', accent);
  }, [theme, accent]);

  const toggleTheme = () => {
    setTheme((prev) => (prev === 'dark' ? 'light' : 'dark'));
  };

  const addToast = (type: 'success' | 'info' | 'warning' | 'error', title: string, message: string) => {
    const id = `toast-${Date.now()}`;
    setToasts((prev) => [...prev, { id, title, message, type }]);
    setTimeout(() => {
      setToasts((prev) => prev.filter((t) => t.id !== id));
    }, 4000);
  };

  const accents = THEME_ACCENT_META;

  // 13 Canonical Design System Categories
  const categories = [
    { id: '01_foundations', num: '01', label: 'Foundations', icon: <Icon name="palette" /> },
    { id: '02_layout', num: '02', label: 'Layout', icon: <Icon name="view_quilt" /> },
    { id: '03_navigation', num: '03', label: 'Navigation', icon: <Icon name="explore" /> },
    { id: '04_actions', num: '04', label: 'Actions', icon: <Icon name="smart_button" /> },
    { id: '05_inputs', num: '05', label: 'Inputs & Forms', icon: <Icon name="edit" /> },
    { id: '06_selection', num: '06', label: 'Selection', icon: <Icon name="check_box" /> },
    { id: '07_surfaces', num: '07', label: 'Surfaces & Containers', icon: <Icon name="layers" /> },
    { id: '08_data_display', num: '08', label: 'Data Display', icon: <Icon name="table_chart" /> },
    { id: '09_visualization', num: '09', label: 'Data Visualization', icon: <Icon name="analytics" /> },
    { id: '10_feedback', num: '10', label: 'Feedback & Status', icon: <Icon name="notification_important" /> },
    { id: '11_overlays', num: '11', label: 'Overlays', icon: <Icon name="picture_in_picture" /> },
    { id: '12_utility', num: '12', label: 'Utility', icon: <Icon name="tune" /> },
    { id: '13_domain', num: '13', label: 'Patterns & Examples', icon: <Icon name="domain" /> },
  ];

  const sampleTableData = [
    { id: 'SRV-101', name: 'Database Cluster Alpha', type: 'PostgreSQL 16', load: '64%', status: 'Active', latency: '24ms' },
    { id: 'SRV-102', name: 'AI Inference Gateway', type: 'TensorRT PyTorch', load: '88%', status: 'High', latency: '48ms' },
    { id: 'SRV-103', name: 'Global Edge CDN Asia', type: 'Anycast DNS', load: '42%', status: 'Active', latency: '12ms' },
    { id: 'SRV-104', name: 'Vault Security Encryptor', type: 'HSM Vault 256', load: '18%', status: 'Optimal', latency: '8ms' },
    { id: 'SRV-105', name: 'Elastic Search Shard', type: 'OpenSearch 2.8', load: '72%', status: 'Active', latency: '35ms' },
  ];

  return (
    <MorphicMotionConfig>
    <div
      style={{
        minHeight: '100vh',
        backgroundColor: 'var(--md-sys-color-background)',
        color: 'var(--md-sys-color-on-background)',
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      {/* Morphic Global Header */}
      <header
        className="morphic-glass-header"
        style={{
          minHeight: '64px',
          padding: '0 32px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          position: 'sticky',
          top: 0,
          zIndex: 100,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
          <div className="mobile-nav-toggle">
            <IconButton
              variant="standard"
              icon={<Icon name={isMobileNavOpen ? 'close' : 'menu'} />}
              onClick={() => setIsMobileNavOpen(!isMobileNavOpen)}
            />
          </div>

          <div
            style={{
              width: '38px',
              height: '38px',
              borderRadius: 'var(--radius-sm)',
              backgroundColor: 'var(--md-sys-color-primary)',
              color: 'var(--md-sys-color-on-primary)',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              fontWeight: 700,
              fontSize: '18px',
              boxShadow: 'var(--md-sys-elevation-level1)',
              flexShrink: 0,
            }}
          >
            FV
          </div>
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap' }}>
              <span style={{ fontWeight: 700, fontSize: '17px', letterSpacing: '-0.02em' }}>Factory Vision</span>
              <span
                style={{
                  fontSize: '10px',
                  padding: '2px 8px',
                  borderRadius: 'var(--radius-pill)',
                  backgroundColor: 'var(--md-sys-color-primary-container)',
                  color: 'var(--md-sys-color-on-primary-container)',
                  fontWeight: 600,
                  textTransform: 'uppercase',
                  letterSpacing: '0.04em',
                }}
              >
                13 Pillars
              </span>
            </div>
          </div>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '16px', flexWrap: 'wrap' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <span style={{ fontSize: '12px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)' }}>
              Accent:
            </span>
            <div style={{ display: 'flex', gap: '6px' }}>
              {accents.map((acc) => (
                <button
                  key={acc.id}
                  onClick={() => setAccent(acc.id)}
                  title={acc.label}
                  style={{
                    width: '22px',
                    height: '22px',
                    borderRadius: '50%',
                    backgroundColor: acc.swatch,
                    border: accent === acc.id ? '2px solid var(--md-sys-color-on-surface)' : '2px solid transparent',
                    boxShadow: accent === acc.id ? '0 0 0 2px var(--md-sys-color-primary)' : 'none',
                    cursor: 'pointer',
                    transition: 'transform 0.15s ease',
                    transform: accent === acc.id ? 'scale(1.15)' : 'scale(1)',
                  }}
                />
              ))}
            </div>
          </div>

          <Button
            variant="tonal"
            size="sm"
            onClick={toggleTheme}
            icon={<Icon name={theme === 'dark' ? 'light_mode' : 'dark_mode'} size={18} />}
          >
            {theme === 'dark' ? 'Light' : 'Dark'}
          </Button>
        </div>
      </header>

      {/* Main Layout Area */}
      <div style={{ display: 'flex', flex: 1, position: 'relative' }}>
        {/* Desktop Navigation Sidebar with 13 Chapters */}
        <aside
          className="showcase-sidebar-desktop"
          style={{
            width: '260px',
            borderRight: '1px solid var(--md-sys-color-border)',
            padding: '20px 14px',
            flexDirection: 'column',
            gap: '4px',
            backgroundColor: 'var(--md-sys-color-surface)',
            position: 'sticky',
            top: '64px',
            height: 'calc(100vh - 64px)',
            overflowY: 'auto',
            flexShrink: 0,
          }}
        >
          <div style={{ padding: '0 12px 8px', fontSize: '11px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)', textTransform: 'uppercase', letterSpacing: '0.06em' }}>
            Design System Taxonomy
          </div>

          {categories.map((cat) => {
            const isActive = activeCategory === cat.id;
            return (
              <button
                key={cat.id}
                onClick={() => setActiveCategory(cat.id)}
                style={{
                  position: 'relative',
                  display: 'flex',
                  alignItems: 'center',
                  gap: '10px',
                  padding: '9px 12px',
                  borderRadius: 'var(--radius-pill)',
                  backgroundColor: 'transparent',
                  color: isActive ? 'var(--md-sys-color-on-primary-container)' : 'var(--md-sys-color-on-surface)',
                  fontWeight: isActive ? 700 : 500,
                  fontSize: '13px',
                  border: 'none',
                  cursor: 'pointer',
                  textAlign: 'left',
                  userSelect: 'none',
                }}
              >
                {isActive && (
                  <motion.div
                    layoutId="activeCategoryPill"
                    transition={M3_SPRING.responsive}
                    style={{
                      position: 'absolute',
                      inset: 0,
                      borderRadius: 'var(--radius-pill)',
                      backgroundColor: 'var(--md-sys-color-primary-container)',
                      zIndex: 0,
                    }}
                  />
                )}
                <span style={{ position: 'relative', zIndex: 1, fontSize: '11px', fontWeight: 600, opacity: 0.6, fontFeatureSettings: '"tnum" 1' }}>
                  {cat.num}
                </span>
                <span style={{ position: 'relative', zIndex: 1, display: 'flex' }}>{cat.icon}</span>
                <span style={{ position: 'relative', zIndex: 1, flex: 1 }}>{cat.label}</span>
              </button>
            );
          })}
        </aside>

        {/* Mobile Navigation Drawer with Overlay */}
        <AnimatePresence>
          {isMobileNavOpen && (
            <>
              <motion.div
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                onClick={() => setIsMobileNavOpen(false)}
                style={{
                  position: 'fixed',
                  inset: 0,
                  backgroundColor: 'rgba(0, 0, 0, 0.5)',
                  zIndex: 998,
                  backdropFilter: 'blur(3px)',
                }}
              />
              <motion.aside
                initial={{ x: '-100%' }}
                animate={{ x: 0 }}
                exit={{ x: '-100%' }}
                transition={M3_TRANSITIONS.enter}
                style={{
                  position: 'fixed',
                  top: 0,
                  bottom: 0,
                  left: 0,
                  width: '280px',
                  backgroundColor: 'var(--md-sys-color-surface)',
                  zIndex: 999,
                  padding: '24px 16px',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: '4px',
                  overflowY: 'auto',
                  boxShadow: 'var(--md-sys-elevation-level3)',
                }}
              >
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px' }}>
                  <span style={{ fontSize: '13px', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.06em' }}>
                    Taxonomy Menu
                  </span>
                  <IconButton variant="standard" icon={<Icon name="close" />} onClick={() => setIsMobileNavOpen(false)} />
                </div>

                {categories.map((cat) => {
                  const isActive = activeCategory === cat.id;
                  return (
                    <button
                      key={cat.id}
                      onClick={() => {
                        setActiveCategory(cat.id);
                        setIsMobileNavOpen(false);
                      }}
                      style={{
                        position: 'relative',
                        display: 'flex',
                        alignItems: 'center',
                        gap: '10px',
                        padding: '10px 14px',
                        borderRadius: 'var(--radius-pill)',
                        backgroundColor: isActive ? 'var(--md-sys-color-primary-container)' : 'transparent',
                        color: isActive ? 'var(--md-sys-color-on-primary-container)' : 'var(--md-sys-color-on-surface)',
                        fontWeight: isActive ? 700 : 500,
                        fontSize: '14px',
                        border: 'none',
                        cursor: 'pointer',
                        textAlign: 'left',
                      }}
                    >
                      <span style={{ fontSize: '12px', fontWeight: 600, opacity: 0.6 }}>{cat.num}</span>
                      {cat.icon}
                      <span style={{ flex: 1 }}>{cat.label}</span>
                    </button>
                  );
                })}
              </motion.aside>
            </>
          )}
        </AnimatePresence>

        {/* Main Content Showcase */}
        <main className="showcase-main-content">
          <div style={{ marginBottom: '20px' }}>
            <Breadcrumbs
              items={[
                { label: 'Morphic Design System', icon: 'home' },
                { label: 'Design System' },
                { label: categories.find((c) => c.id === activeCategory)?.label || 'Module' },
              ]}
            />
          </div>

          {/* 01. FOUNDATIONS */}
          {activeCategory === '01_foundations' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>01. Foundations</h1>
              </div>

              <Section title="Color Palette & Dynamic Accent Tokens">
                <Grid columns={5} gap="md">
                  {accents.map((acc) => (
                    <Card key={acc.id} variant="outlined" padding="md">
                      <div style={{ height: '48px', borderRadius: 'var(--radius-md)', backgroundColor: acc.swatch, marginBottom: '10px' }} />
                      <div style={{ fontSize: '14px', fontWeight: 700 }}>{acc.label}</div>
                      <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)' }}>{acc.swatch}</div>
                    </Card>
                  ))}
                </Grid>
              </Section>

              <Section title="Shape Scale (Border Radii)">
                <Grid columns={4} gap="md">
                  {[
                    { label: '--radius-hero (28px)', radius: 'var(--radius-hero)' },
                    { label: '--radius-xl (22px)', radius: 'var(--radius-xl)' },
                    { label: '--radius-lg (18px)', radius: 'var(--radius-lg)' },
                    { label: '--radius-pill (999px)', radius: 'var(--radius-pill)' },
                  ].map((s, i) => (
                    <div key={i} style={{ padding: '16px', borderRadius: s.radius, backgroundColor: 'var(--md-sys-color-surface-container)', border: '1px solid var(--md-sys-color-border)', textAlign: 'center' }}>
                      <span style={{ fontSize: '12px', fontWeight: 600 }}>{s.label}</span>
                    </div>
                  ))}
                </Grid>
              </Section>

              <Section title="Elevation & Depth Scale">
                <Grid columns={5} gap="md">
                  {[1, 2, 3, 4, 5].map((lvl) => (
                    <div key={lvl} style={{ padding: '24px 16px', borderRadius: 'var(--radius-lg)', backgroundColor: 'var(--md-sys-color-surface)', border: '1px solid var(--md-sys-color-border)', boxShadow: `var(--md-sys-elevation-level${lvl})`, textAlign: 'center' }}>
                      <div style={{ fontSize: '13px', fontWeight: 700 }}>Level {lvl}</div>
                    </div>
                  ))}
                </Grid>
              </Section>

              <Section title="Material Transitions & Easing Curves">
                <Card variant="outlined" padding="lg">
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--md-sys-spacing-2)' }}>
                    {[
                      ['hover', 'hover', '140ms'],
                      ['button', 'button', '160ms'],
                      ['card', 'card', '200ms'],
                      ['enter', 'enter', '250ms'],
                      ['page', 'page', '300ms'],
                      ['chart', 'chart', '550ms'],
                    ].map(([label, name, ms]) => (
                      <div key={label} style={{ display: 'flex', alignItems: 'center', gap: 'var(--md-sys-spacing-3)' }}>
                        <span style={{ width: '72px', fontSize: 'var(--md-sys-typescale-meta-size)', color: 'var(--md-sys-color-on-surface-variant)' }}>
                          {label}
                        </span>
                        <div style={{ flex: 1, height: '8px', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-surface-container-high)', overflow: 'hidden' }}>
                          <motion.div
                            key={`${label}-${transitionKey}`}
                            initial={{ scaleX: 0 }}
                            animate={{ scaleX: 1 }}
                            transition={M3_TRANSITIONS[name as keyof typeof M3_TRANSITIONS]}
                            style={{ height: '100%', transformOrigin: 'left', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-primary)' }}
                          />
                        </div>
                        <span style={{ width: '48px', textAlign: 'right', fontSize: 'var(--md-sys-typescale-meta-size-sm)', fontVariantNumeric: 'tabular-nums', color: 'var(--md-sys-color-on-surface-variant)' }}>
                          {ms}
                        </span>
                      </div>
                    ))}
                  </div>
                  <div style={{ marginTop: '16px' }}>
                    <Button variant="tonal" size="sm" onClick={() => setTransitionKey((k) => k + 1)}>
                      Replay Transitions
                    </Button>
                  </div>
                </Card>
              </Section>

              <Section title="Motion Physics & Interactive Springs">
                <Grid columns={2} gap="lg">
                  <Card variant="outlined" padding="lg">
                    <h4 style={{ margin: '0 0 12px' }}>Interactive Spring Physics</h4>
                    <SpringPlayground />
                  </Card>
                  <Card variant="outlined" padding="lg">
                    <h4 style={{ margin: '0 0 12px' }}>Spring Presets Preview</h4>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '12px', padding: '12px 0' }}>
                      {(Object.keys(M3_SPRING) as Array<keyof typeof M3_SPRING>).map((name) => (
                        <div key={name} style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
                          <span style={{ width: '72px', fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>{name}</span>
                          <motion.div
                            animate={{ x: springToggled ? 140 : 0 }}
                            transition={M3_SPRING[name]}
                            style={{
                              width: '20px',
                              height: '20px',
                              borderRadius: '50%',
                              backgroundColor: name === 'playful' ? 'var(--md-sys-color-chart-tertiary)' : 'var(--md-sys-color-primary)',
                            }}
                          />
                        </div>
                      ))}
                    </div>
                    <Button variant="tonal" size="sm" onClick={() => setSpringToggled((v) => !v)}>
                      Toggle Springs
                    </Button>
                  </Card>
                </Grid>
              </Section>
            </Stack>
          )}

          {/* 02. LAYOUT */}
          {activeCategory === '02_layout' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>02. Layout</h1>
              </div>

              <Section title="SplitLayout (Responsive Multi-Column)">
                <SplitLayout
                  ratio="60/40"
                  primary={
                    <Card variant="outlined" padding="lg">
                      <h4 style={{ margin: '0 0 8px' }}>Primary Content Column (60%)</h4>
                      <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                        Main operational stream, data table, or primary form container.
                      </p>
                    </Card>
                  }
                  secondary={
                    <Card variant="outlined" padding="lg">
                      <h4 style={{ margin: '0 0 8px' }}>Secondary Telemetry (40%)</h4>
                      <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                        Sidebar insights, live metrics, and context actions.
                      </p>
                    </Card>
                  }
                />
              </Section>

              <Section title="Flex Stack & Row Primitives">
                <Card variant="outlined" padding="lg">
                  <Stack gap="md">
                    <Row gap="md" wrap>
                      <Button variant="filled">Row Item 1</Button>
                      <Button variant="tonal">Row Item 2</Button>
                      <Button variant="outlined">Row Item 3</Button>
                    </Row>
                  </Stack>
                </Card>
              </Section>

              <Section title="Shared Layout & Dynamic Tab Indicator">
                <Card variant="outlined" padding="lg">
                  <LayoutGroup id="layout-demo-tabs">
                    <div
                      style={{
                        display: 'inline-flex',
                        gap: 'var(--md-sys-spacing-1)',
                        padding: 'var(--md-sys-spacing-1)',
                        borderRadius: 'var(--radius-pill)',
                        backgroundColor: 'var(--md-sys-color-surface-container)',
                        marginBottom: '16px',
                      }}
                    >
                      {['Overview', 'Activity', 'Settings'].map((tab) => (
                        <button
                          key={tab}
                          type="button"
                          onClick={() => setLayoutTabActive(tab)}
                          style={{
                            position: 'relative',
                            border: 'none',
                            background: 'none',
                            cursor: 'pointer',
                            padding: 'var(--md-sys-spacing-2) var(--md-sys-spacing-4)',
                            borderRadius: 'var(--radius-pill)',
                            fontSize: 'var(--md-sys-typescale-label-size)',
                            fontWeight: 550,
                            color: layoutTabActive === tab ? 'var(--md-sys-color-on-primary-container)' : 'var(--md-sys-color-on-surface-variant)',
                          }}
                        >
                          <SharedIndicator active={layoutTabActive === tab} groupId="layout-demo-tabs" radius="var(--radius-pill)" />
                          <span style={{ position: 'relative' }}>{tab}</span>
                        </button>
                      ))}
                    </div>
                  </LayoutGroup>

                  <Grid columns={2} gap="lg">
                    <LayoutTransformDemo />
                    <StaggerList />
                  </Grid>
                </Card>
              </Section>

              <Section title="Scroll-Linked & Progressive Reveal Animations">
                <Card variant="outlined" padding="lg">
                  <div
                    style={{
                      height: 220,
                      overflowY: 'auto',
                      borderRadius: 'var(--radius-lg)',
                      backgroundColor: 'var(--md-sys-color-surface-container)',
                      padding: 'var(--md-sys-spacing-4)',
                      display: 'flex',
                      flexDirection: 'column',
                      gap: 'var(--md-sys-spacing-3)',
                    }}
                  >
                    <div style={{ height: 20 }} />
                    <Reveal direction="up">
                      <div style={{ padding: '14px', borderRadius: 'var(--radius-md)', backgroundColor: 'var(--md-sys-color-surface)', border: '1px solid var(--md-sys-color-border)', fontWeight: 550, fontSize: '13px' }}>
                        Reveal — fades and rises smoothly into view
                      </div>
                    </Reveal>
                    <StaggerReveal stagger="normal" style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                      <div style={{ padding: '12px', borderRadius: 'var(--radius-md)', backgroundColor: 'var(--md-sys-color-surface)', border: '1px solid var(--md-sys-color-border)', fontSize: '13px' }}>StaggerReveal Row 1</div>
                      <div style={{ padding: '12px', borderRadius: 'var(--radius-md)', backgroundColor: 'var(--md-sys-color-surface)', border: '1px solid var(--md-sys-color-border)', fontSize: '13px' }}>StaggerReveal Row 2</div>
                    </StaggerReveal>
                    <Parallax distance={20}>
                      <div style={{ padding: '14px', borderRadius: 'var(--radius-md)', backgroundColor: 'var(--md-sys-color-surface)', border: '1px solid var(--md-sys-color-border)', fontWeight: 550, fontSize: '13px' }}>
                        Parallax — drifts softly against container scroll
                      </div>
                    </Parallax>
                    <ScrollScale>
                      <div style={{ padding: '14px', borderRadius: 'var(--radius-md)', backgroundColor: 'var(--md-sys-color-surface)', border: '1px solid var(--md-sys-color-border)', fontWeight: 550, fontSize: '13px' }}>
                        ScrollScale — settles scale cleanly as it enters viewport
                      </div>
                    </ScrollScale>
                    <div style={{ height: 20 }} />
                  </div>
                </Card>
              </Section>
            </Stack>
          )}

          {/* 03. NAVIGATION */}
          {activeCategory === '03_navigation' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>03. Navigation</h1>
              </div>

              <Section title="Top App Bar Shell">
                <div style={{ borderRadius: 'var(--radius-xl)', overflow: 'hidden', border: '1px solid var(--md-sys-color-border)' }}>
                  <TopAppBar
                    title="Morphic App Shell"
                    variant="center-aligned"
                    leadingIcon={<Icon name="menu" />}
                    actions={
                      <>
                        <IconButton variant="standard" icon={<Icon name="search" />} />
                        <IconButton variant="standard" icon={<Icon name="more_vert" />} />
                      </>
                    }
                  />
                </div>
              </Section>

              <Section title="Tabs & Pagination">
                <Card variant="outlined" padding="lg">
                  <Stack gap="lg">
                    <Tabs
                      tabs={[
                        { id: 'tab1', label: 'Executive Summary', icon: <Icon name="dashboard" size={18} /> },
                        { id: 'tab2', label: 'Portfolio Analytics', icon: <Icon name="insights" size={18} /> },
                        { id: 'tab3', label: 'Compliance Audit', icon: <Icon name="verified_user" size={18} /> },
                      ]}
                      activeTab={tabActive}
                      onChange={setTabActive}
                    />

                    <Pagination
                      currentPage={paginationPage}
                      totalPages={10}
                      totalEntities={96}
                      pageSize={10}
                      onPageChange={setPaginationPage}
                    />
                  </Stack>
                </Card>
              </Section>
            </Stack>
          )}

          {/* 04. ACTIONS */}
          {activeCategory === '04_actions' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>04. Actions</h1>
              </div>

              <Section title="Split Buttons & Button Groups">
                <Card variant="outlined" padding="lg">
                  <Row gap="lg" wrap>
                    <SplitButton
                      label="Publish Release"
                      icon={<Icon name="rocket_launch" size={16} />}
                      variant="filled"
                      items={[
                        { id: '1', label: 'Save as Draft', icon: 'draft' },
                        { id: '2', label: 'Schedule Deployment', icon: 'schedule' },
                        { id: '3', label: 'Export Configuration', icon: 'download' },
                      ]}
                    />

                    <ButtonGroup variant="attached">
                      <Button variant="tonal">Monthly</Button>
                      <Button variant="tonal">Quarterly</Button>
                      <Button variant="tonal">Annual</Button>
                    </ButtonGroup>
                  </Row>
                </Card>
              </Section>

              <Section title="Common Action Buttons & FABs">
                <Card variant="outlined" padding="lg">
                  <Row gap="md" wrap>
                    <Button variant="elevated">Elevated</Button>
                    <Button variant="filled">Filled</Button>
                    <Button variant="tonal">Filled Tonal</Button>
                    <Button variant="outlined">Outlined</Button>
                    <Button variant="text">Text</Button>
                    <FAB size="standard" icon={<Icon name="add" size={24} />} label="Extended FAB" />
                  </Row>
                </Card>
              </Section>

              <Section title="Interactive Gestures & Micro-interactions">
                <Grid columns={3} gap="lg">
                  <Card variant="outlined" padding="lg">
                    <h4 style={{ margin: '0 0 12px' }}>Pressable Micro-physics</h4>
                    <Pressable lift as="div">
                      <div style={{ padding: '20px', borderRadius: 'var(--radius-md)', backgroundColor: 'var(--md-sys-color-surface-container)', border: '1px solid var(--md-sys-color-border)', textAlign: 'center', fontWeight: 600 }}>
                        Hover lifts, press scales 0.98
                      </div>
                    </Pressable>
                  </Card>

                  <Card variant="outlined" padding="lg">
                    <h4 style={{ margin: '0 0 12px' }}>Magnetic Cursor Pull</h4>
                    <div style={{ display: 'flex', justifyContent: 'center', padding: '12px 0' }}>
                      <Magnetic strength={0.3}>
                        <Button variant="filled" icon={<Icon name="near_me" size={16} />}>
                          Magnetic Action
                        </Button>
                      </Magnetic>
                    </div>
                  </Card>

                  <Card variant="outlined" padding="lg">
                    <h4 style={{ margin: '0 0 12px' }}>3D Tilt Perspective</h4>
                    <div style={{ display: 'flex', justifyContent: 'center' }}>
                      <Tilt max={10}>
                        <div style={{ width: '140px', height: '80px', borderRadius: 'var(--radius-lg)', background: 'var(--hero-banner-bg)', color: 'var(--hero-banner-text)', display: 'grid', placeItems: 'center', fontWeight: 600, fontSize: '13px' }}>
                          Tilt 3D Hover
                        </div>
                      </Tilt>
                    </div>
                  </Card>
                </Grid>
              </Section>
            </Stack>
          )}

          {/* 05. INPUTS & FORMS */}
          {activeCategory === '05_inputs' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>05. Inputs & Forms</h1>
              </div>

              <Section title="Dropdown Select & MultiSelect">
                <Grid columns={2} gap="lg">
                  <Select
                    label="Infrastructure Category"
                    value={selectedCategoryValue}
                    onChange={setSelectedCategoryValue}
                    searchable
                    options={[
                      { value: 'tech', label: 'Cloud Infrastructure & API', icon: 'cloud' },
                      { value: 'db', label: 'Database & Storage Vault', icon: 'storage' },
                      { value: 'sec', label: 'Cyber Security & Encryption', icon: 'security' },
                      { value: 'ai', label: 'AI & Machine Learning Nodes', icon: 'memory' },
                    ]}
                  />

                  <MultiSelect
                    label="Installed Modules & Stack"
                    values={selectedTags}
                    onChange={setSelectedTags}
                    options={[
                      { value: 'react', label: 'React 18' },
                      { value: 'design_system', label: 'Material 3 Tokens' },
                      { value: 'motion', label: 'Motion.dev Engine' },
                      { value: 'typescript', label: 'TypeScript Strict' },
                    ]}
                  />
                </Grid>
              </Section>

              <Section title="Text Inputs, Stepper & Form Controls">
                <Card variant="outlined" padding="lg">
                  <Grid columns={2} gap="lg">
                    <FilledTextField label="Project Entity Name" leadingIcon={<Icon name="badge" />} />
                    <NumberInput label="Server Node Quantity" value={nodeCount} onChange={setNodeCount} min={1} max={50} unit="Units" />
                  </Grid>
                  <div style={{ marginTop: '20px' }}>
                    <TextArea label="Documentation & Notes" value={textComment} onChange={(e) => setTextComment(e.target.value)} maxLength={200} />
                  </div>
                </Card>
              </Section>
            </Stack>
          )}

          {/* 06. SELECTION */}
          {activeCategory === '06_selection' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>06. Selection</h1>
              </div>

              <Section title="FilterBar Primitive">
                <FilterBar
                  activeFilter={activeFilterId}
                  onFilterChange={setActiveFilterId}
                  onClearAll={() => setActiveFilterId('all')}
                  filters={[
                    { id: 'all', label: 'All Status' },
                    { id: 'online', label: 'Online', count: 142 },
                    { id: 'warning', label: 'Warning', count: 4 },
                    { id: 'maintenance', label: 'Maintenance', count: 2 },
                  ]}
                  rightSlot={
                    <ToggleGroup
                      value={toggleGroupVal}
                      onChange={setToggleGroupVal}
                      items={[
                        { value: 'grid', icon: 'grid_view' },
                        { value: 'list', icon: 'view_list' },
                      ]}
                    />
                  }
                />
              </Section>

              <Section title="Date & Range Pickers (Official M3 Modal)">
                <Card variant="outlined" padding="lg">
                  <Row gap="md" wrap>
                    <Button variant="filled" icon={<Icon name="calendar_today" size={18} />} onClick={() => setIsDatePickerOpen(true)}>
                      Open Single DatePicker
                    </Button>
                    <Button variant="tonal" icon={<Icon name="date_range" size={18} />} onClick={() => setIsDateRangePickerOpen(true)}>
                      Open Date Range Picker
                    </Button>
                    <Button variant="outlined" icon={<Icon name="schedule" size={18} />} onClick={() => setIsTimePickerOpen(true)}>
                      Open TimePicker Modal
                    </Button>
                  </Row>
                </Card>
              </Section>
            </Stack>
          )}

          {/* 07. SURFACES & CONTAINERS */}
          {activeCategory === '07_surfaces' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>07. Surfaces & Containers</h1>
              </div>

              {/* 1. Hero Card Showcase */}
              <Section title="Hero Card (Morphic Art Canvas)">
                <Stack gap="lg">
                  {/* 1. Factory Vision Primary Hero Card with Cursor Spotlight & Live Metrics */}
                  <HeroCard
                    badgeLabel="FACTORY VISION WORKSPACE"
                    badgeIcon="auto_awesome"
                    greeting="Good morning"
                    userName="Alex"
                    subtitle="Start your day with clean records and real-time operational telemetry."
                    variant="brand"
                    metric={{ label: 'Monthly Revenue', value: '$128,450', trend: '+14.2%' }}
                    actions={
                      <Row gap="sm">
                        <Button variant="filled" icon={<Icon name="download" size={16} />}>Export Data</Button>
                        <Button variant="tonal" icon={<Icon name="picture_as_pdf" size={16} />}>Download Report</Button>
                      </Row>
                    }
                  />

                  {/* 2. Factory Vision Infrastructure Telemetry */}
                  <HeroCard
                    badgeLabel="FACTORY VISION INFRASTRUCTURE"
                    badgeIcon="hub"
                    title="Enterprise Telemetry & Real-Time Performance"
                    subtitle="Monitoring 142 active infrastructure cluster nodes across 6 global latency zones."
                    variant="brand"
                    metric={{ label: 'Cluster Throughput', value: '48.2 GB/s', trend: '+8.4%' }}
                    primaryActionLabel="Open Analytics"
                    secondaryActionLabel="Cluster Settings"
                    onPrimaryAction={() => setActiveCategory('09_visualization')}
                    onSecondaryAction={() => setIsGenericModalOpen(true)}
                  />
                </Stack>
              </Section>

              {/* 2. Core Card Variations */}
              <Section title="Core Card Variants (Basic, Elevated, Filled, Outlined, Interactive)">
                <Grid columns={3} gap="md">
                  <Card variant="elevated" clickable>
                    <h3 style={{ margin: '0 0 8px', fontSize: '16px' }}>Elevated Card</h3>
                    <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                      Features Elevation Level 1 at rest and smooth Level 2 shadow on hover.
                    </p>
                  </Card>
                  <Card variant="filled" clickable>
                    <h3 style={{ margin: '0 0 8px', fontSize: '16px' }}>Filled Card</h3>
                    <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                      Surface-container container color without high-contrast outlines.
                    </p>
                  </Card>
                  <Card variant="outlined" clickable>
                    <h3 style={{ margin: '0 0 8px', fontSize: '16px' }}>Outlined Card</h3>
                    <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                      Clean 1px border with a flat surface background.
                    </p>
                  </Card>
                </Grid>
              </Section>

              {/* 3. SectionCard & Collapsible Panel */}
              <Section title="Structured SectionCard & Collapsible Panel">
                <Grid columns={2} gap="lg">
                  <SectionCard
                    title="Audit Security Cluster"
                    subtitle="Automated vulnerability scan running daily"
                    action={<Button variant="tonal" size="sm">Configure</Button>}
                  >
                    <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)', lineHeight: 1.5 }}>
                      Zero critical vulnerabilities detected across 142 enterprise compute nodes in last scan.
                    </p>
                  </SectionCard>

                  <Panel
                    title="Advanced Network Routing"
                    subtitle="Manage BGP Anycast routes & firewall"
                    badge="Active"
                    icon="router"
                    defaultExpanded
                  >
                    <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                      Multi-region Anycast DNS routing active with 12ms average edge latency.
                    </p>
                  </Panel>
                </Grid>
              </Section>

              {/* 4. Overlays & Sheets */}
              <Section title="Modal Sheets">
                <Card variant="outlined" padding="lg">
                  <Row gap="md" wrap>
                    <Button variant="tonal" icon={<Icon name="vertical_align_top" size={18} />} onClick={() => setIsSheetOpen(true)}>
                      Open Bottom Sheet
                    </Button>
                    <Button variant="outlined" icon={<Icon name="dock" size={18} />} onClick={() => setIsSideSheetOpen(true)}>
                      Open Side Sheet (400px)
                    </Button>
                  </Row>
                </Card>
              </Section>
            </Stack>
          )}

          {/* 08. DATA DISPLAY */}
          {activeCategory === '08_data_display' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>08. Data Display</h1>
              </div>

              <Section title="Stats & Status Badges">
                <Grid columns={4} gap="md">
                  <Card variant="outlined" padding="md">
                    <Stat label="Total Volume" value="$2.48M" change="+14.2%" trend="up" />
                  </Card>
                  <Card variant="outlined" padding="md">
                    <Stat label="Avg Latency" value="18 ms" change="-3.1ms" trend="up" />
                  </Card>
                  <Card variant="outlined" padding="md">
                    <Stat label="Uptime SLA" value="99.99%" trend="neutral" />
                  </Card>
                  <Card variant="outlined" padding="md">
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                      <span style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 600 }}>Active Nodes</span>
                      <StatusBadge status="online" label="142 Online" />
                      <StatusBadge status="warning" label="2 Degraded" />
                    </div>
                  </Card>
                </Grid>
              </Section>

              <Section title="Enterprise Advanced Data Table">
                <AdvancedDataTable
                  title="Active Infrastructure Server Nodes"
                  subtitle="Compute load telemetry and cluster latency monitoring"
                  data={sampleTableData}
                  columns={[
                    { key: 'id', header: 'Server ID', sortable: true, width: '120px' },
                    { key: 'name', header: 'Entity Name', sortable: true },
                    { key: 'type', header: 'Engine Type', sortable: true },
                    { key: 'load', header: 'CPU Load', sortable: true },
                    { key: 'latency', header: 'Latency', sortable: true },
                    {
                      key: 'status',
                      header: 'Status',
                      render: (row) => <StatusBadge status={row.status === 'Active' || row.status === 'Optimal' ? 'online' : 'warning'} label={row.status} size="sm" />,
                    },
                  ]}
                />
              </Section>

              <Section title="Kinetic Typography & Counter Telemetry">
                <Card variant="outlined" padding="lg">
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
                    <SplitText
                      key={`chars-${textSeed}`}
                      type="chars"
                      trigger="load"
                      style={{ fontSize: '22px', fontWeight: 700, letterSpacing: '-0.02em' }}
                    >
                      Kinetic Character Reveal
                    </SplitText>

                    <SplitText
                      key={`words-${textSeed}`}
                      type="words"
                      stagger="normal"
                      trigger="load"
                      style={{ fontSize: '15px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)' }}
                    >
                      Precision telemetry updates synchronized with fluid typography
                    </SplitText>

                    <div style={{ display: 'flex', alignItems: 'baseline', gap: '20px', flexWrap: 'wrap' }}>
                      <ScrambleText key={`scramble-${textSeed}`} text="RESOLVING STREAM" style={{ fontSize: '13px', fontWeight: 600 }} />
                      <span style={{ fontSize: '24px', fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--md-sys-color-primary)' }}>
                        <CountUp key={`count-${textSeed}`} end={8492500} prefix="Rp " locale="id-ID" />
                      </span>
                    </div>

                    <div>
                      <Button variant="tonal" size="sm" onClick={() => setTextSeed((s) => s + 1)}>
                        Replay Typography
                      </Button>
                    </div>
                  </div>
                </Card>
              </Section>
            </Stack>
          )}

          {/* 09. DATA VISUALIZATION */}
          {activeCategory === '09_visualization' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>09. Data Visualization</h1>
              </div>

              <Grid columns={3} gap="lg">
                <ChartCard title="System Efficiency" subtitle="Real-time cluster throughput" controls={<ChartControls activePeriod={activeDatePeriod} onPeriodChange={setActiveDatePeriod} />}>
                  <GaugeChart value={86} title="Operational Health" subtitle="Target 95% SLA" />
                </ChartCard>

                <ChartCard title="Cloud Budget Allocation">
                  <PieChart
                    slices={[
                      { label: 'Compute', value: 45, color: 'var(--md-sys-color-primary)' },
                      { label: 'Storage', value: 25, color: 'var(--md-sys-color-info)' },
                      { label: 'Security', value: 20, color: 'var(--md-sys-color-chart-neutral)' },
                      { label: 'CDN', value: 10, color: 'var(--md-sys-color-warning)' },
                    ]}
                  />
                </ChartCard>

                <ChartCard title="360° Radar Performance">
                  <RadarChart
                    metrics={[
                      { label: 'Speed', value: 92 },
                      { label: 'Security', value: 98 },
                      { label: 'Stability', value: 85 },
                      { label: 'Cost', value: 74 },
                      { label: 'SLA', value: 96 },
                    ]}
                  />
                </ChartCard>
              </Grid>

              <Section title="Weekly Activity Density Heatmap">
                <HeatmapGrid />
              </Section>

              <Section title="Dynamic Vector & Path Drawing Animations">
                <Card variant="outlined" padding="lg">
                  <Grid columns={3} gap="lg">
                    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '12px' }}>
                      <DrawSVG
                        key={`line-${svgSeed}`}
                        d="M 0 96 C 34 96, 44 24, 76 40 S 118 96, 148 52 S 186 12, 200 28"
                        width={200}
                        height={100}
                        viewBox="0 0 200 120"
                        strokeWidth={3}
                        label="Revenue path"
                      />
                      <span style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 600 }}>Path Interpolation</span>
                    </div>

                    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '12px' }}>
                      <AnimatedCircularProgress key={`ring-${svgSeed}`} value={svgProgress} size={96} strokeWidth={10} label="Completion">
                        <span style={{ fontSize: '16px', fontWeight: 700, fontVariantNumeric: 'tabular-nums' }}>
                          {svgProgress}%
                        </span>
                      </AnimatedCircularProgress>
                      <span style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 600 }}>Animated Gauge</span>
                    </div>

                    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: '12px' }}>
                      <SvgDrawMotion trigger="hover" />
                      <span style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 600 }}>Hover Vector Morph</span>
                    </div>
                  </Grid>

                  <div style={{ display: 'flex', gap: '12px', marginTop: '16px', flexWrap: 'wrap' }}>
                    <Button variant="tonal" size="sm" onClick={() => setSvgSeed((s) => s + 1)}>
                      Redraw Vectors
                    </Button>
                    <Button
                      variant="text"
                      size="sm"
                      onClick={() => setSvgProgress(Math.round(Math.min(100, Math.max(10, svgProgress + (Math.random() * 50 - 25)))))}
                    >
                      Randomize Metric
                    </Button>
                  </div>
                </Card>
              </Section>
            </Stack>
          )}

          {/* 10. FEEDBACK & STATUS */}
          {activeCategory === '10_feedback' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>10. Feedback & Status</h1>
              </div>

              <Stack gap="md">
                <InfoBanner
                  title="Cluster Hardening Update Available (v14.2.0)"
                  description="A new hardened configuration snapshot is ready for deployment across all regional nodes."
                  actionLabel="Deploy Update"
                  onAction={() => addToast('success', 'Update Triggered', 'Deployment job queued successfully.')}
                />

                <WarningBanner
                  title="Vault Storage Approaching 85% Capacity"
                  description="Expand persistent volume or purge stale audit telemetry."
                  actionLabel="Manage Storage"
                />
              </Stack>

              <Grid columns={2} gap="lg">
                <SuccessState
                  title="Snapshot Created Successfully"
                  description="Encrypted backup snapshot is synchronized across all 3 redundant cloud datacenters."
                />
                <ErrorState
                  title="Gateway Port Timeout"
                  description="Network socket dropped on port 443. Reconnection attempt will retry in 5s."
                />
              </Grid>
            </Stack>
          )}

          {/* 11. OVERLAYS */}
          {activeCategory === '11_overlays' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>11. Overlays</h1>
              </div>

              <Card variant="outlined" padding="lg">
                <Row gap="md" wrap>
                  <Button variant="filled" onClick={() => setIsGenericModalOpen(true)}>
                    Open Modal (28px Radius)
                  </Button>
                  <Button variant="tonal" onClick={() => setIsSheetOpen(true)}>
                    Open Bottom Sheet
                  </Button>
                  <Button variant="outlined" onClick={() => setIsSideSheetOpen(true)}>
                    Open Side Sheet (400px)
                  </Button>

                  <Popover
                    trigger={<Button variant="tonal">Open Popover</Button>}
                    content={
                      <div>
                        <h4 style={{ margin: '0 0 6px', fontSize: '14px' }}>Quick Telemetry Info</h4>
                        <p style={{ margin: 0, fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                          This popover is anchored directly to the trigger button.
                        </p>
                      </div>
                    }
                  />
                </Row>
              </Card>

              <Section title="Presence & Smooth Transition Lists">
                <Card variant="outlined" padding="lg">
                  <div style={{ display: 'flex', gap: '8px', marginBottom: '16px', flexWrap: 'wrap' }}>
                    <Button variant="tonal" size="sm" onClick={() => setPresenceOpen((v) => !v)}>
                      {presenceOpen ? 'Collapse Panel' : 'Expand Panel'}
                    </Button>
                    <Button
                      variant="tonal"
                      size="sm"
                      onClick={() => {
                        const id = presenceNext.current++;
                        setPresenceRows((r) => [{ id, label: `Audit Log Event #${id}` }, ...r]);
                      }}
                    >
                      Add Event Row
                    </Button>
                    <Button variant="text" size="sm" onClick={() => setPresenceRows((r) => r.slice(1))}>
                      Remove Top Row
                    </Button>
                  </div>

                  <Presence show={presenceOpen} preset="collapse">
                    <div style={{ padding: '16px', borderRadius: 'var(--radius-lg)', backgroundColor: 'var(--md-sys-color-surface-container)', marginBottom: '16px' }}>
                      Animated collapsible presence with fluid height transition and exit animations.
                    </div>
                  </Presence>

                  <PresenceList
                    items={presenceRows}
                    getKey={(r) => r.id}
                    preset="fadeUp"
                    style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}
                  >
                    {(row) => (
                      <div style={{ padding: '12px 16px', borderRadius: 'var(--radius-md)', backgroundColor: 'var(--md-sys-color-surface-container)', fontSize: '13px', display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <Icon name="history" size={16} />
                        <span>{row.label}</span>
                      </div>
                    )}
                  </PresenceList>
                </Card>
              </Section>
            </Stack>
          )}

          {/* 12. UTILITY */}
          {activeCategory === '12_utility' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>12. Utility</h1>
              </div>

              <Card variant="outlined" padding="lg">
                <Stack gap="lg">
                  <Row gap="md" justify="space-between" wrap>
                    <SearchBar value={searchValue} onChange={setSearchValue} placeholder="Search anything in system..." />
                    <DateFilter activePeriod={activeDatePeriod} onChange={setActiveDatePeriod} />
                  </Row>

                  <Row gap="md" justify="space-between" wrap>
                    <Row gap="sm">
                      <ExportButton
                        onExportExcel={() => addToast('success', 'Export Started', 'Generating Excel workbook...')}
                        onExportPdf={() => addToast('info', 'Export Started', 'Rendering PDF report...')}
                      />
                      <RefreshButton
                        isLoading={refreshLoading}
                        onRefresh={() => {
                          setRefreshLoading(true);
                          setTimeout(() => setRefreshLoading(false), 1200);
                        }}
                      />
                    </Row>

                    <Row gap="md">
                      <SortControl
                        sortKey={sortField}
                        sortOrder={sortDir}
                        options={[
                          { key: 'name', label: 'Entity Name' },
                          { key: 'load', label: 'CPU Load' },
                          { key: 'latency', label: 'Response Latency' },
                        ]}
                        onSortChange={(k, o) => { setSortField(k); setSortDir(o); }}
                      />
                      <ViewSwitcher currentView={currentViewMode} onViewChange={setCurrentViewMode} />
                    </Row>
                  </Row>
                </Stack>
              </Card>

              <Section title="Interactive Playback Scrubber & Particle FX">
                <Grid columns={2} gap="lg">
                  <Card variant="outlined" padding="lg">
                    <h4 style={{ margin: '0 0 12px' }}>Timeline Scrubber</h4>
                    <div style={{ height: 90, display: 'flex', alignItems: 'center', backgroundColor: 'var(--md-sys-color-surface-container)', borderRadius: 'var(--radius-md)', padding: '16px', marginBottom: '12px' }}>
                      <div
                        ref={scrubberBoxRef}
                        style={{
                          width: 36,
                          height: 36,
                          borderRadius: 'var(--radius-md)',
                          backgroundColor: 'var(--md-sys-color-primary)',
                        }}
                      />
                    </div>
                    <MotionScrubber animation={scrubberControls} />
                  </Card>

                  <Card variant="outlined" padding="lg">
                    <h4 style={{ margin: '0 0 12px' }}>Particle Burst Celebration</h4>
                    <p style={{ margin: '0 0 16px', fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                      Trigger confetti physics particle effect on demand:
                    </p>
                    <ParticleBurst />
                  </Card>
                </Grid>
              </Section>
            </Stack>
          )}

          {/* 13. DOMAIN COMPONENTS */}
          {activeCategory === '13_domain' && (
            <Stack gap="xl">
              <div>
                <h1 style={{ margin: 0, fontSize: '28px', fontWeight: 700 }}>13. Patterns &amp; Examples</h1>
              </div>

              <StatusOverview
                metrics={[
                  { label: '30-day uptime', value: '99.99%', emphasis: true },
                  { label: 'Active nodes', value: '142/144' },
                  { label: 'API success', value: '99.96%', emphasis: true },
                ]}
              />

              <MapCard />

              <Grid columns={2} gap="lg">
                <WarehouseCard
                  name="Jakarta Central Logistics Hub"
                  code="JKT-DC-01"
                  location="West Java Region"
                  occupancyRate={78}
                  activeShipments={1240}
                  totalCapacity="50,000 m²"
                />

                <DeliveryCard
                  trackingId="SHP-8921-X"
                  origin="Warehouse DC-01"
                  destination="Surabaya Fulfillment"
                  eta="Today, 18:30 WIB"
                  progress={75}
                  carrier="Express Logistics Air"
                  status="In Transit"
                />
              </Grid>

              <AlertSummary
                title="Alerts"
                alerts={[
                  { id: '1', title: 'High CPU Load on Node Alpha-3', source: 'Cluster Monitoring', time: '5m ago', severity: 'high' },
                  { id: '2', title: 'Disk Usage Reached 82%', source: 'Vault Storage', time: '22m ago', severity: 'medium' },
                ]}
                onAcknowledge={(id: string) => addToast('info', 'Alert Dismissed', `Alert ${id} acknowledged.`)}
              />
            </Stack>
          )}

          {/* Shared Modals & Overlays */}
          <Modal isOpen={isGenericModalOpen} onClose={() => setIsGenericModalOpen(false)} title="System Configuration">
            <Stack gap="md">
              <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                Configure global telemetry sampling rates and cluster sync frequencies.
              </p>
              <FilledTextField label="Sampling Rate (ms)" value="250" />
              <Row justify="flex-end" gap="sm">
                <Button variant="text" onClick={() => setIsGenericModalOpen(false)}>Cancel</Button>
                <Button variant="filled" onClick={() => setIsGenericModalOpen(false)}>Save Changes</Button>
              </Row>
            </Stack>
          </Modal>

          <BottomSheet isOpen={isSheetOpen} onClose={() => setIsSheetOpen(false)} title="Quick Actions Sheet">
            <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>
              Drag-to-dismiss bottom sheet for quick mobile actions.
            </p>
          </BottomSheet>

          <SideSheet isOpen={isSideSheetOpen} onClose={() => setIsSideSheetOpen(false)} title="Filter Telemetry">
            <Stack gap="md">
              <FilledTextField label="Keyword Filter" leadingIcon={<Icon name="search" />} />
              <Switch checked={switchChecked} onChange={setSwitchChecked} label="Verified Nodes Only" />
            </Stack>
          </SideSheet>

          <DatePickerModal isOpen={isDatePickerOpen} onClose={() => setIsDatePickerOpen(false)} />
          <DateRangePickerModal isOpen={isDateRangePickerOpen} onClose={() => setIsDateRangePickerOpen(false)} />
          <TimePickerModal isOpen={isTimePickerOpen} onClose={() => setIsTimePickerOpen(false)} />

          {/* Floating Toast Notification Container */}
          <div style={{ position: 'fixed', bottom: '24px', right: '24px', zIndex: 1000, display: 'flex', flexDirection: 'column', gap: '8px' }}>
            {toasts.map((t) => (
              <Toast key={t.id} toast={t} onDismiss={(id) => setToasts((prev) => prev.filter((item) => item.id !== id))} />
            ))}
          </div>
        </main>
      </div>
    </div>
    </MorphicMotionConfig>
  );
}
