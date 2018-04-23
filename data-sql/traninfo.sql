DROP TABLE IF EXISTS `traninfo`;
CREATE TABLE `traninfo` (
  `tranNum` varchar(10) NOT NULL COMMENT '车次号',
  `depTime` time NOT NULL COMMENT '发车时间',
  `costTime` time NOT NULL COMMENT '总耗时',
  `runDays` int(11) NOT NULL COMMENT '行驶天数'
) ENGINE=InnoDB DEFAULT CHARSET=utf8 COMMENT='车次信息';
insert  into `traninfo`(`tranNum`,`depTime`,`costTime`,`runDays`) values 