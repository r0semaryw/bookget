// router/interface.go
package router

import (
	"bookget/app"
	"bookget/config"
	"bookget/pkg/util"
	"errors"
	"strings"
	"sync"
)

type RouterInit interface {
	GetRouterInit(sUrl string) (map[string]interface{}, error)
}

var (
	Router        = make(map[string]RouterInit)
	routerFactory = make(map[string]func() RouterInit)
	doInit        sync.Once
)

// registerRouter keeps the public router table for compatibility while also
// retaining a constructor.  A fresh router instance prevents state from one
// URL (book id, page counter, cookie jar, and so on) leaking into the next
// batch item.
func registerRouter[T RouterInit](siteID string, constructor func() T) {
	Router[siteID] = constructor()
	routerFactory[siteID] = func() RouterInit { return constructor() }
}

// FactoryRouter 创建路由器的工厂函数
func FactoryRouter(siteID string, sUrl string) (map[string]interface{}, error) {
	// 自动检测逻辑
	if config.Conf.DownloaderMode == 1 {
		siteID = "bookget"
	} else if config.Conf.DownloaderMode == 2 || strings.Contains(sUrl, ".json") {
		siteID = "iiif.io"
	}

	if strings.Contains(sUrl, "tiles/infos.json") {
		siteID = "dzicnlib"
	}

	// 初始化路由器(线程安全)
	doInit.Do(func() {
		//[中国]国家图书馆
		registerRouter("read.nlc.cn", app.NewChinaNlc)
		registerRouter("mylib.nlc.cn", app.NewChinaNlc)
		registerRouter("guji.nlc.cn", app.NewNlcGuji)

		//[中国] CADAL大学数字图书馆国际合作计划
		registerRouter("cadal.edu.cn", app.NewCadan)
		registerRouter("www.cadal.edu.cn", app.NewCadan)

		//[中国]山东大学图书馆特藏资源
		registerRouter("tclib.sdu.edu.cn", app.NewSduTclib)
		registerRouter("www.tclib.sdu.edu.cn", app.NewSduTclib)

		//[中国]识典古籍
		registerRouter("www.shidianguji.com", app.NewShidian)
		registerRouter("shidianguji.com", app.NewShidian)

		//[中国]臺灣華文電子書庫
		registerRouter("taiwanebook.ncl.edu.tw", app.NewHuawen)

		//[中国]香港中文大学图书馆
		registerRouter("repository.lib.cuhk.edu.hk", app.NewCuhk)

		//[中国]香港科技大学图书馆
		registerRouter("lbezone.hkust.edu.hk", app.NewUsthk)

		//[中国]洛阳市图书馆
		registerRouter("111.7.82.29:8090", app.NewLuoyang)

		//[中国]温州市图书馆
		registerRouter("oyjy.wzlib.cn", app.NewWzlib)
		registerRouter("arcgxhpv7cw0.db.wzlib.cn", app.NewWzlib)

		//[中国]深圳市图书馆-古籍
		registerRouter("yun.szlib.org.cn", app.NewSzLib)

		//[中国]广州大典
		registerRouter("gzdd.gzlib.gov.cn", app.NewGzlib)
		registerRouter("gzdd.gzlib.org.cn", app.NewGzlib)

		//[中国]天一阁博物院古籍数字化平台
		registerRouter("gj.tianyige.com.cn", app.NewTianyige)

		//[中国]江苏高校珍贵古籍数字图书馆
		registerRouter("jsgxgj.nju.edu.cn", app.NewNjuedu)

		//[中国]中华寻根网-国图
		registerRouter("ouroots.nlc.cn", app.NewOuroots)

		//[中国]国家哲学社会科学文献中心
		registerRouter("www.ncpssd.org", app.NewNcpssd)
		registerRouter("www.ncpssd.cn", app.NewNcpssd)

		//[中国]山东中医药大学古籍数字图书馆
		registerRouter("gjsztsg.sdutcm.edu.cn", app.NewSdutcm)
		//[中国]山东省古籍数字资源平台
		registerRouter("guji.sdlib.com", app.NewSdlib)

		//[中国]天津图书馆历史文献数字资源库
		registerRouter("lswx.tjl.tj.cn:8001", app.NewTjlswx)

		//[中国]云南数字方志馆
		registerRouter("dfz.yn.gov.cn", app.NewYndfz)

		//[中国]香港大学数字图书
		registerRouter("digitalrepository.lib.hku.hk", app.NewHkulib)

		//[中国]山东省诸城市图书馆
		registerRouter("124.134.220.209:8100", app.NewZhuCheng)
		//[中国]中央美术学院
		registerRouter("dlibgate.cafa.edu.cn", app.NewCafaEdu)
		registerRouter("dlib.cafa.edu.cn", app.NewCafaEdu)

		//抗日战争与中日关系文献数据平台
		registerRouter("www.modernhistory.org.cn", app.NewWar1931)

		//[中国]甘肃省图书馆
		registerRouter("zszy.gslib.com.cn", app.NewGslib)
		//}}} -----------------------------------------------------------------

		//---------------日本--------------------------------------------------
		//[日本]国立国会图书馆
		registerRouter("dl.ndl.go.jp", app.NewNdlJP)

		//[日本]E国宝eMuseum
		registerRouter("emuseum.nich.go.jp", app.NewEmuseum)

		//[日本]宫内厅书陵部（汉籍集览）
		registerRouter("db2.sido.keio.ac.jp", app.NewKeio)

		//[日本]东京大学东洋文化研究所（汉籍善本资料库）
		registerRouter("shanben.ioc.u-tokyo.ac.jp", app.NewUtokyo)

		//[日本]国立公文书馆（内阁文库）
		registerRouter("www.digital.archives.go.jp", app.NewNationaljp)

		//[日本]东洋文库
		registerRouter("dsr.nii.ac.jp", app.NewNiiac)

		//[日本]早稻田大学图书馆
		registerRouter("archive.wul.waseda.ac.jp", app.NewWaseda)

		//[日本]国書数据库（古典籍）
		registerRouter("kokusho.nijl.ac.jp", app.NewKokusho)

		//[日本]京都大学人文科学研究所 东方学数字图书博物馆
		registerRouter("kanji.zinbun.kyoto-u.ac.jp", app.NewKyotou)

		//[日本]駒澤大学 电子贵重书库
		registerRouter("repo.komazawa-u.ac.jp", app.NewIiifRouter)

		//[日本]关西大学图书馆
		registerRouter("www.iiif.ku-orcas.kansai-u.ac.jp", app.NewIiifRouter)

		//[日本]庆应义塾大学图书馆
		registerRouter("dcollections.lib.keio.ac.jp", app.NewIiifRouter)

		//[日本]国立历史民俗博物馆
		registerRouter("khirin-a.rekihaku.ac.jp", app.NewKhirin)

		//[日本]市立米泽图书馆
		registerRouter("www.library.yonezawa.yamagata.jp", app.NewYonezawa)
		registerRouter("webarchives.tnm.jp", app.NewTnm)

		//[日本]龙谷大学
		registerRouter("da.library.ryukoku.ac.jp", app.NewRyukoku)
		//}}} -----------------------------------------------------------------

		//{{{---------------美国、欧洲--------------------------------------------------
		//[美国]哈佛大学图书馆
		registerRouter("iiif.lib.harvard.edu", app.NewHarvard)
		registerRouter("listview.lib.harvard.edu", app.NewHarvard)
		registerRouter("curiosity.lib.harvard.edu", app.NewHarvard)

		//[美国]hathitrust 数字图书馆
		registerRouter("babel.hathitrust.org", app.NewHathitrust)

		//[美国]普林斯顿大学图书馆
		registerRouter("catalog.princeton.edu", app.NewPrinceton)
		registerRouter("dpul.princeton.edu", app.NewPrinceton)

		//[美国]国会图书馆
		registerRouter("www.loc.gov", app.NewLoc)

		//[美国]斯坦福大学图书馆

		//[美国]犹他州家谱
		registerRouter("www.familysearch.org", app.NewFamilysearch)

		//[美国]archive.org
		registerRouter("archive.org", app.NewArchive)

		//[德国]柏林国立图书馆
		registerRouter("digital.staatsbibliothek-berlin.de", app.NewBerlin)

		//[德国]巴伐利亞州立圖書館東亞數字資源庫
		registerRouter("ostasien.digitale-sammlungen.de", app.NewSammlungen)
		registerRouter("www.digitale-sammlungen.de", app.NewSammlungen)

		//[英国]牛津大学博德利图书馆
		registerRouter("digital.bodleian.ox.ac.uk", app.NewOxacuk)

		//[英国]图书馆文本手稿
		registerRouter("www.bl.uk", app.NewBluk)

		//Smithsonian Institution
		registerRouter("ids.si.edu", app.NewSiEdu)
		registerRouter("www.si.edu", app.NewSiEdu)
		registerRouter("iiif.si.edu", app.NewSiEdu)
		registerRouter("asia.si.edu", app.NewSiEdu)

		//[美國]柏克萊加州大學東亞圖書館
		registerRouter("digicoll.lib.berkeley.edu", app.NewBerkeley)

		//奥地利国图
		registerRouter("digital.onb.ac.at", app.NewOnbDigital)
		//}}} -----------------------------------------------------------------

		//{{{---------------其它--------------------------------------------------
		//國際敦煌項目
		registerRouter("idp.nlc.cn", app.NewIdp)
		registerRouter("idp.bl.uk", app.NewIdp)
		registerRouter("idp.orientalstudies.ru", app.NewIdp)
		registerRouter("idp.afc.ryukoku.ac.jp", app.NewIdp)
		registerRouter("idp.bbaw.de", app.NewIdp)
		registerRouter("idp.bnf.fr", app.NewIdp)
		registerRouter("idp.korea.ac.kr", app.NewIdp)

		//[韩国]
		registerRouter("kyudb.snu.ac.kr", app.NewKyudbSnu)
		registerRouter("lod.nl.go.kr", app.NewLodNLGoKr)

		//高丽大学
		registerRouter("kostma.korea.ac.kr", app.NewKorea)

		//俄罗斯图书馆
		registerRouter("viewer.rsl.ru", app.NewRslRu)

		//越南汉喃古籍文献典藏数位计划
		registerRouter("lib.nomfoundation.org", app.NewNomfoundation)

		//越南国家图书馆汉农图书馆
		registerRouter("hannom.nlv.gov.vn", app.NewHannomNlv)
		//}}} -----------------------------------------------------------------

		registerRouter("bookget", app.NewImageDownloader)
		registerRouter("dzicnlib", app.NewDziCnLib)
		registerRouter("iiif.io", app.NewIiifRouter)
	})

	// 检查路由器是否存在
	if _, ok := Router[siteID]; !ok {
		urlType := util.GetHeaderContentType(sUrl)
		if urlType == "json" {
			siteID = "iiif.io"
		} else if urlType == "bookget" {
			siteID = "bookget"
		}

		if _, ok := Router[siteID]; !ok {
			return nil, errors.New("unsupported URL: " + sUrl)
		}
	}

	if constructor, ok := routerFactory[siteID]; ok {
		return constructor().GetRouterInit(sUrl)
	}
	return Router[siteID].GetRouterInit(sUrl)

}
