package model

type Dynasty string

const (
	DynastyTang    Dynasty = "唐"
	DynastySong    Dynasty = "宋"
	DynastyYuan    Dynasty = "元"
	DynastyFive    Dynasty = "五代"
	DynastyChu     Dynasty = "楚"
	DynastyCaoCao  Dynasty = "汉"
	DynastyQing    Dynasty = "清"
	DynastyClassic Dynasty = "经"
	DynastyNone    Dynasty = ""
)

type Author struct {
	ID      string
	Name    string
	Dynasty Dynasty
	Desc    string
}

type Poem struct {
	ID       string
	Dynasty  Dynasty
	Author   string
	Title    string
	Rhythmic string
	Content  string
	Tags     string
	Chapter  string
	Section  string
	Type     string // "poem" or "text"
}

type Dataset struct {
	Name        string
	Dir         string
	Dynasty     Dynasty
	Type        string // "poem", "ci", "text", "classic"
	HasAuthors  bool
	AuthorFile  string
	PoemFiles   []string
	PoemPattern string
	Count       int
	// AutoDir datasets have no remote source: every *.json under the dir is
	// imported from local disk (files produced by tools/crawler).
	AutoDir bool
}

var Datasets = []Dataset{
	{
		Name:        "全唐诗",
		Dir:         "全唐诗",
		Dynasty:     DynastyTang,
		Type:        "poem",
		HasAuthors:  true,
		AuthorFile:  "authors.tang.json",
		PoemPattern: "poet.tang.%d.json",
		Count:       58000,
	},
	{
		Name:        "全宋诗",
		Dir:         "全宋诗",
		Dynasty:     DynastySong,
		Type:        "poem",
		HasAuthors:  true,
		AuthorFile:  "authors.song.json",
		PoemPattern: "poet.song.%d.json",
		Count:       255000,
	},
	{
		Name:        "宋词",
		Dir:         "宋词",
		Dynasty:     DynastySong,
		Type:        "ci",
		HasAuthors:  true,
		AuthorFile:  "author.song.json",
		PoemPattern: "ci.song.%d.json",
		Count:       22000,
	},
	{
		Name:      "元曲",
		Dir:       "元曲",
		Dynasty:   DynastyYuan,
		Type:      "poem",
		PoemFiles: []string{"yuanqu.json"},
		Count:     6000,
	},
	{
		Name:      "五代诗词",
		Dir:       "五代诗词",
		Dynasty:   DynastyFive,
		Type:      "poem",
		PoemFiles: []string{"五代诗词.json"},
		Count:     500,
	},
	{
		Name:      "花间集",
		Dir:       "五代诗词",
		Dynasty:   DynastyFive,
		Type:      "ci",
		PoemFiles: []string{"huajianji.json"},
		Count:     600,
	},
	{
		Name:      "南唐二主词",
		Dir:       "五代诗词",
		Dynasty:   DynastyFive,
		Type:      "ci",
		PoemFiles: []string{"nantang.json"},
		Count:     50,
	},
	{
		// 仓库的"四书五经"目录实际只含大学、中庸、孟子3本（无论语），按章节拆分共16章
		// 论语和诗经在独立目录，已作为单独数据集
		// 尚书、礼记、周易、春秋仓库中无数据
		Name:      "四书（部分）",
		Dir:       "四书五经",
		Dynasty:   DynastyClassic,
		Type:      "classic",
		PoemFiles: []string{"daxue.json", "mengzi.json", "zhongyong.json"},
		Count:     16,
	},
	{
		Name:      "论语",
		Dir:       "论语",
		Dynasty:   DynastyClassic,
		Type:      "classic",
		PoemFiles: []string{"lunyu.json"},
		Count:     200,
	},
	{
		Name:      "诗经",
		Dir:       "诗经",
		Dynasty:   DynastyClassic,
		Type:      "classic",
		PoemFiles: []string{"shijing.json"},
		Count:     305,
	},
	{
		Name:      "蒙学",
		Dir:       "蒙学",
		Dynasty:   DynastyClassic,
		Type:      "classic",
		PoemFiles: []string{"mengxue.json", "erkeyi.json", "sanyiing.json", "sanzi-jing.json"},
		Count:     50,
	},
	{
		Name:      "幽梦影",
		Dir:       "幽梦影",
		Dynasty:   DynastyClassic,
		Type:      "classic",
		PoemFiles: []string{"youmengying.json"},
		Count:     100,
	},
	{
		Name:      "纳兰性德",
		Dir:       "纳兰性德",
		Dynasty:   DynastyQing,
		Type:      "poem",
		PoemFiles: []string{"nalan.json"},
		Count:     700,
	},
	{
		Name:      "御定全唐詩",
		Dir:       "御定全唐詩",
		Dynasty:   DynastyTang,
		Type:      "poem",
		PoemFiles: []string{"yuding.json"},
		Count:     10000,
	},
	{
		Name:      "楚辞",
		Dir:       "楚辞",
		Dynasty:   DynastyChu,
		Type:      "classic",
		PoemFiles: []string{"chuci.json"},
		Count:     169,
	},
	{
		Name:      "曹操诗集",
		Dir:       "曹操诗集",
		Dynasty:   DynastyCaoCao,
		Type:      "poem",
		PoemFiles: []string{"caocao.json"},
		Count:     30,
	},
	{
		Name:      "水墨唐诗",
		Dir:       "水墨唐诗",
		Dynasty:   DynastyTang,
		Type:      "poem",
		PoemFiles: []string{"shuimo.json"},
		Count:     500,
	},
	{
		// 维基文库经典，由 tools/crawler 抓取生成 JSON 后放入数据目录的
		// "维基文库" 子目录（内容已转换为简体）
		Name:    "维基文库",
		Dir:     "维基文库",
		Dynasty: DynastyClassic,
		Type:    "classic",
		AutoDir: true,
	},
}

func GetDataset(name string) (Dataset, bool) {
	for _, ds := range Datasets {
		if ds.Name == name {
			return ds, true
		}
	}
	return Dataset{}, false
}
